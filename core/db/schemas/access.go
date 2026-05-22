// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Schema for the safety/access package — Unified Access Control
// (TS 24.501 §4.5) gating + Forbidden TAI / Forbidden PLMN lists
// (TS 24.501 §5.3.13 / §5.3.13A).
//
// Three tables, all operator-scoped:
//
//   - access_forbidden_tai:   per-PLMN+TAC entries the AMF must
//                             refuse Initial-Registration from.
//   - access_forbidden_plmn:  PLMNs the AMF must refuse altogether.
//   - access_uac_barring:     Unified Access Control barring per
//                             access category (NAS reject backoff).
//
// Audit log of admission decisions:
//
//   - access_decision_log:    every CheckAccess() result (allow /
//                             deny) with the §clause we used to
//                             refuse.

package schemas

func init() {
	Register("access", AccessDDL)
}

var AccessDDL = []string{
	`CREATE TABLE IF NOT EXISTS access_forbidden_tai (
      id          INTEGER PRIMARY KEY AUTOINCREMENT,
      plmn_id     TEXT NOT NULL,
      tac         TEXT NOT NULL,
      reason      TEXT NOT NULL DEFAULT '',
      added_at    TEXT NOT NULL DEFAULT (datetime('now')),
      added_by    TEXT NOT NULL DEFAULT 'operator',
      UNIQUE(plmn_id, tac)
    )`,

	`CREATE TABLE IF NOT EXISTS access_forbidden_plmn (
      id          INTEGER PRIMARY KEY AUTOINCREMENT,
      plmn_id     TEXT NOT NULL UNIQUE,
      reason      TEXT NOT NULL DEFAULT '',
      added_at    TEXT NOT NULL DEFAULT (datetime('now')),
      added_by    TEXT NOT NULL DEFAULT 'operator'
    )`,

	`CREATE TABLE IF NOT EXISTS access_uac_barring (
      id                INTEGER PRIMARY KEY AUTOINCREMENT,
      access_category   INTEGER NOT NULL UNIQUE,
      barring_factor    REAL NOT NULL DEFAULT 1.0,
      barring_time_s    INTEGER NOT NULL DEFAULT 0,
      enabled           INTEGER NOT NULL DEFAULT 1,
      updated_at        TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS access_decision_log (
      id          INTEGER PRIMARY KEY AUTOINCREMENT,
      imsi        TEXT NOT NULL DEFAULT '',
      plmn_id     TEXT NOT NULL DEFAULT '',
      tac         TEXT NOT NULL DEFAULT '',
      decision    TEXT NOT NULL CHECK (decision IN ('allow','deny')),
      reason      TEXT NOT NULL DEFAULT '',
      created_at  TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE INDEX IF NOT EXISTS idx_access_fbd_tai_plmn ON access_forbidden_tai(plmn_id)`,
	`CREATE INDEX IF NOT EXISTS idx_access_dec_log_ts  ON access_decision_log(created_at)`,
}

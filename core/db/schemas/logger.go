// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
// db/schemas/logger.go — Logger configuration tables
package schemas

var LoggerDDL = []string{
	`CREATE TABLE IF NOT EXISTS logger_config (
      module          TEXT PRIMARY KEY,
      grp             TEXT NOT NULL DEFAULT 'Other',
      enabled         INTEGER NOT NULL DEFAULT 1,
      level           TEXT NOT NULL DEFAULT 'INFO'
                      CHECK (level IN ('DEBUG','INFO','WARNING','ERROR','CRITICAL')),
      description     TEXT NOT NULL DEFAULT '',
      log_count       INTEGER NOT NULL DEFAULT 0
    )`,

	`CREATE TABLE IF NOT EXISTS logger_presets (
      name            TEXT PRIMARY KEY,
      description     TEXT NOT NULL DEFAULT '',
      config          TEXT NOT NULL DEFAULT '{}',
      is_builtin      INTEGER NOT NULL DEFAULT 0
    )`,
}

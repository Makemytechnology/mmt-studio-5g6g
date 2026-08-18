// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
// db/schemas/fm.go — Fault Management DDL (TS 28.532, ITU-T X.733)
package schemas

var FMDDL = []string{
	`CREATE TABLE IF NOT EXISTS alarms (
      alarm_id           INTEGER PRIMARY KEY,
      managed_object     TEXT NOT NULL,
      alarm_type         TEXT NOT NULL,
      probable_cause     TEXT NOT NULL,
      perceived_severity TEXT NOT NULL,
      specific_problem   TEXT NOT NULL,
      additional_text    TEXT NOT NULL DEFAULT '',
      additional_info    TEXT NOT NULL DEFAULT '',
      event_time         TEXT NOT NULL DEFAULT (datetime('now')),
      last_raised        TEXT NOT NULL DEFAULT (datetime('now')),
      clear_time         TEXT,
      ack_state          TEXT NOT NULL DEFAULT 'Unacknowledged',
      ack_time           TEXT,
      ack_user           TEXT,
      raise_count        INTEGER NOT NULL DEFAULT 1
    )`,
	`CREATE INDEX IF NOT EXISTS idx_alarm_severity ON alarms(perceived_severity)`,
	`CREATE INDEX IF NOT EXISTS idx_alarm_moi ON alarms(managed_object)`,
}

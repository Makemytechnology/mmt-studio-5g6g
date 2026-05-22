// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
// db/schemas/ims.go — IMS DDL (TS 23.228)
package schemas

var IMSDDL = []string{
	`CREATE TABLE IF NOT EXISTS ims_service_profiles (
      id                    INTEGER PRIMARY KEY AUTOINCREMENT,
      name                  TEXT UNIQUE NOT NULL,
      filter_criteria_json  TEXT NOT NULL DEFAULT '[]'
    )`,

	`CREATE TABLE IF NOT EXISTS ims_dialogs (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      call_id         TEXT NOT NULL,
      from_tag        TEXT NOT NULL,
      to_tag          TEXT,
      from_uri        TEXT NOT NULL,
      to_uri          TEXT NOT NULL,
      state           TEXT NOT NULL DEFAULT 'INIT',
      sdp_offer       TEXT,
      sdp_answer      TEXT,
      created_at      TEXT NOT NULL DEFAULT (datetime('now')),
      UNIQUE(call_id, from_tag)
    )`,
	`CREATE INDEX IF NOT EXISTS idx_ims_dlg_callid ON ims_dialogs(call_id)`,
}

// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
// db/schemas/positioning.go — Positioning / Location Services DDL
// TS 23.273, TS 29.572, TS 23.271, TS 38.455
package schemas

var PositioningDDL = []string{
	`CREATE TABLE IF NOT EXISTS positioning_sessions (
        id              INTEGER PRIMARY KEY AUTOINCREMENT,
        session_id      TEXT UNIQUE NOT NULL,
        imsi            TEXT NOT NULL,
        method          TEXT NOT NULL CHECK (method IN ('auto','ecid','multi_rtt','dl_tdoa','ul_tdoa','dl_aod','ul_aoa','agnss')),
        state           TEXT NOT NULL DEFAULT 'PENDING' CHECK (state IN ('PENDING','IN_PROGRESS','COMPLETED','FAILED','CANCELLED')),
        latitude        REAL,
        longitude       REAL,
        altitude        REAL,
        uncertainty_m   REAL,
        confidence      REAL,
        qos_accuracy    REAL,
        qos_response_time REAL,
        created_at      TEXT NOT NULL DEFAULT (datetime('now')),
        completed_at    TEXT
    )`,
	`CREATE INDEX IF NOT EXISTS idx_pos_sess_imsi ON positioning_sessions(imsi)`,
	`CREATE INDEX IF NOT EXISTS idx_pos_sess_state ON positioning_sessions(state)`,
	`CREATE INDEX IF NOT EXISTS idx_pos_sess_sid ON positioning_sessions(session_id)`,

	`CREATE TABLE IF NOT EXISTS location_history (
        id              INTEGER PRIMARY KEY AUTOINCREMENT,
        imsi            TEXT NOT NULL,
        latitude        REAL NOT NULL,
        longitude       REAL NOT NULL,
        altitude        REAL,
        uncertainty_m   REAL,
        method          TEXT,
        source          TEXT NOT NULL DEFAULT 'lmf' CHECK (source IN ('lmf','gnss','ecid')),
        timestamp       TEXT NOT NULL DEFAULT (datetime('now'))
    )`,
	`CREATE INDEX IF NOT EXISTS idx_loc_hist_imsi ON location_history(imsi)`,
	`CREATE INDEX IF NOT EXISTS idx_loc_hist_ts ON location_history(timestamp)`,

	`CREATE TABLE IF NOT EXISTS geofences (
        id              INTEGER PRIMARY KEY AUTOINCREMENT,
        name            TEXT NOT NULL,
        imsi            TEXT,
        center_lat      REAL NOT NULL,
        center_lon      REAL NOT NULL,
        radius_m        REAL NOT NULL,
        trigger_type    TEXT NOT NULL DEFAULT 'both' CHECK (trigger_type IN ('enter','leave','both')),
        callback_url    TEXT,
        active          INTEGER NOT NULL DEFAULT 1,
        created_at      TEXT NOT NULL DEFAULT (datetime('now'))
    )`,
	`CREATE INDEX IF NOT EXISTS idx_geofence_imsi ON geofences(imsi)`,
	`CREATE INDEX IF NOT EXISTS idx_geofence_active ON geofences(active)`,

	`CREATE TABLE IF NOT EXISTS lcs_privacy (
        id              INTEGER PRIMARY KEY AUTOINCREMENT,
        imsi            TEXT NOT NULL,
        client_type     TEXT NOT NULL CHECK (client_type IN ('emergency','commercial','lawful_intercept')),
        allowed         INTEGER NOT NULL DEFAULT 1,
        created_at      TEXT NOT NULL DEFAULT (datetime('now')),
        UNIQUE(imsi, client_type)
    )`,
	`CREATE INDEX IF NOT EXISTS idx_lcs_priv_imsi ON lcs_privacy(imsi)`,
}

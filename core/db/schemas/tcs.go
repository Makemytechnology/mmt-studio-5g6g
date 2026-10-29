// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
// db/schemas/tcs.go — Tactical Communication System DDL
// TS 23.287, TS 38.300
package schemas

var TCSDDL = []string{
	`CREATE TABLE IF NOT EXISTS ue_location (
        supi                TEXT PRIMARY KEY,
        serving_node_id     TEXT NOT NULL,
        serving_node_ip     TEXT NOT NULL,
        ims_contact_uri     TEXT,
        mcx_endpoint        TEXT,
        status              TEXT NOT NULL DEFAULT 'ATTACHED' CHECK (status IN ('ATTACHED','IDLE','DETACHED')),
        lamport_clock       INTEGER NOT NULL DEFAULT 0,
        updated_at          TEXT NOT NULL DEFAULT (datetime('now'))
    )`,
	`CREATE INDEX IF NOT EXISTS idx_ue_loc_node ON ue_location(serving_node_id)`,
	`CREATE INDEX IF NOT EXISTS idx_ue_loc_status ON ue_location(status)`,

	`CREATE TABLE IF NOT EXISTS sync_peers (
        node_id             TEXT PRIMARY KEY,
        node_ip             TEXT NOT NULL,
        sidelink_l2_id      TEXT,
        ims_sip_uri         TEXT,
        mcx_endpoint        TEXT,
        link_status         TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (link_status IN ('ACTIVE','DEGRADED','UNREACHABLE')),
        node_mode           TEXT NOT NULL DEFAULT 'full_nib' CHECK (node_mode IN ('full_nib','gnb_only')),
        sidelink_rsrp       REAL,
        last_seen           TEXT NOT NULL DEFAULT (datetime('now'))
    )`,
	`CREATE INDEX IF NOT EXISTS idx_sync_peers_status ON sync_peers(link_status)`,

	`CREATE TABLE IF NOT EXISTS db_changes (
        id                  INTEGER PRIMARY KEY AUTOINCREMENT,
        table_name          TEXT NOT NULL,
        row_key             TEXT NOT NULL,
        column_name         TEXT NOT NULL,
        new_value           TEXT,
        lamport_clock       INTEGER NOT NULL,
        node_id             TEXT NOT NULL,
        version             INTEGER NOT NULL,
        applied_at          TEXT NOT NULL DEFAULT (datetime('now'))
    )`,
	`CREATE INDEX IF NOT EXISTS idx_db_changes_ver ON db_changes(version)`,
	`CREATE INDEX IF NOT EXISTS idx_db_changes_tbl ON db_changes(table_name, row_key)`,
	`CREATE INDEX IF NOT EXISTS idx_db_changes_clock ON db_changes(lamport_clock, node_id)`,
}

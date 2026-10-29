// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Schema for the access/wifi_offload package — operator-side state
// for non-3GPP (WLAN) access via N3IWF / TNGF (TS 23.501 §6.2.9
// and §6.2.9A). Three tables:
//
//   - wifi_access_policy:    per-DNN trust + offload policy.
//   - wifi_attached_ues:     in-flight WLAN-attached UE table.
//   - wifi_offload_log:      attach / detach audit trail.

package schemas

func init() {
	Register("wifi_offload", WifiOffloadDDL)
}

var WifiOffloadDDL = []string{
	`CREATE TABLE IF NOT EXISTS wifi_access_policy (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      dnn             TEXT NOT NULL,
      access_type     TEXT NOT NULL DEFAULT 'untrusted'
                      CHECK (access_type IN ('untrusted','trusted','wireline')),
      offload_pref    TEXT NOT NULL DEFAULT '5g_first'
                      CHECK (offload_pref IN ('5g_first','wlan_first','5g_only','wlan_only','atsss')),
      enabled         INTEGER NOT NULL DEFAULT 1,
      updated_at      TEXT NOT NULL DEFAULT (datetime('now')),
      UNIQUE(dnn)
    )`,

	`CREATE TABLE IF NOT EXISTS wifi_attached_ues (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      imsi            TEXT NOT NULL,
      access_type     TEXT NOT NULL
                      CHECK (access_type IN ('untrusted','trusted','wireline')),
      n3iwf_id        TEXT NOT NULL DEFAULT '',
      inner_ip        TEXT NOT NULL DEFAULT '',
      outer_ip        TEXT NOT NULL DEFAULT '',
      attached_at     TEXT NOT NULL DEFAULT (datetime('now')),
      UNIQUE(imsi, access_type)
    )`,

	`CREATE TABLE IF NOT EXISTS wifi_offload_log (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      imsi            TEXT NOT NULL DEFAULT '',
      access_type     TEXT NOT NULL DEFAULT '',
      action          TEXT NOT NULL CHECK (action IN ('attached','detached','rejected')),
      reason          TEXT NOT NULL DEFAULT '',
      created_at      TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE INDEX IF NOT EXISTS idx_wifi_attached_imsi ON wifi_attached_ues(imsi)`,
	`CREATE INDEX IF NOT EXISTS idx_wifi_offload_log_ts ON wifi_offload_log(created_at)`,
}

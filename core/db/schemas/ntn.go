// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Schema for the access/ntn package — Non-Terrestrial Network
// Phase 2 operator state.
//
// Tables:
//
//   - ntn_regenerative_config: per-satellite onboard NF profile for
//     regenerative payload satellites (TS 23.501 §5.4.11.9,
//     TS 38.821 §5.2). `onboard_nfs` is a JSON-encoded list (e.g.
//     ["AMF","UPF"]) carried as TEXT.
//
//   - ntn_store_forward: queued downlink data waiting for the
//     serving LEO to come into ground-station contact
//     (TS 23.501 §5.4.13 discontinuous coverage; TR 38.821 S&F).
//     Lifecycle: queued → forwarded | expired.
//
//   - ntn_isl_links: operator-visible inter-satellite link adjacency
//     (TS 23.501 §5.4.14). Pair is normalized so (sat1, sat2) is
//     stored with sat1_id < sat2_id and is unique.
package schemas

func init() {
	Register("ntn", NTNDDL)
}

var NTNDDL = []string{
	`CREATE TABLE IF NOT EXISTS ntn_regenerative_config (
      id                  INTEGER PRIMARY KEY AUTOINCREMENT,
      sat_id              TEXT NOT NULL UNIQUE,
      onboard_nfs         TEXT NOT NULL DEFAULT '[]',
      processing_capacity INTEGER NOT NULL DEFAULT 0,
      memory_mb           INTEGER NOT NULL DEFAULT 0,
      status              TEXT NOT NULL DEFAULT 'standby',
      created_at          TEXT NOT NULL DEFAULT (datetime('now')),
      updated_at          TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS ntn_store_forward (
      id            INTEGER PRIMARY KEY AUTOINCREMENT,
      sat_id        TEXT NOT NULL,
      target        TEXT NOT NULL,
      data_hex      TEXT NOT NULL DEFAULT '',
      data_size     INTEGER NOT NULL DEFAULT 0,
      priority      INTEGER NOT NULL DEFAULT 0,
      status        TEXT NOT NULL DEFAULT 'queued'
                       CHECK (status IN ('queued','forwarded','expired')),
      created_at    TEXT NOT NULL DEFAULT (datetime('now')),
      forwarded_at  TEXT
    )`,

	`CREATE TABLE IF NOT EXISTS ntn_isl_links (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      sat1_id         TEXT NOT NULL,
      sat2_id         TEXT NOT NULL,
      bandwidth_mbps  INTEGER NOT NULL DEFAULT 0,
      latency_ms      REAL NOT NULL DEFAULT 0,
      status          TEXT NOT NULL DEFAULT 'inactive',
      created_at      TEXT NOT NULL DEFAULT (datetime('now')),
      UNIQUE(sat1_id, sat2_id)
    )`,

	`CREATE INDEX IF NOT EXISTS idx_ntn_sf_status ON ntn_store_forward(status)`,
	`CREATE INDEX IF NOT EXISTS idx_ntn_sf_sat    ON ntn_store_forward(sat_id, status)`,
	`CREATE INDEX IF NOT EXISTS idx_ntn_isl_sat1  ON ntn_isl_links(sat1_id)`,
	`CREATE INDEX IF NOT EXISTS idx_ntn_isl_sat2  ON ntn_isl_links(sat2_id)`,
}

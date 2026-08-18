// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
// db/schemas/smf.go — SMF / UPF registry tables.
//
// TS 23.501 §6.3.3 — UPF selection (SMF picks by DNN / S-NSSAI / load).
// TS 29.244 — PFCP (Sx/N4) between SMF and UPF.
package schemas

var SMFDDL = []string{
	// SMF↔UPF is always PFCP/N4 (TS 29.244). interface_type +
	// rest_port were removed — on upgrades the columns remain on
	// existing rows unused (SQLite can't DROP COLUMN cleanly) but
	// the registry CRUD no longer reads or writes them.
	`CREATE TABLE IF NOT EXISTS upf_instances (
      upf_id          TEXT PRIMARY KEY,
      upf_ip          TEXT NOT NULL,
      n3_ip           TEXT NOT NULL,
      n6_ip           TEXT,
      pfcp_port       INTEGER NOT NULL DEFAULT 8805,
      supported_dnns  TEXT NOT NULL DEFAULT 'internet',
      supported_sst   TEXT NOT NULL DEFAULT '01',
      max_sessions    INTEGER NOT NULL DEFAULT 100000,
      registered_at   TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS pfcp_associations (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      upf_id          TEXT NOT NULL,
      cp_node_id      TEXT NOT NULL,
      up_node_id      TEXT NOT NULL,
      recovery_time   TEXT,
      features        TEXT,
      established_at  TEXT NOT NULL DEFAULT (datetime('now')),
      FOREIGN KEY (upf_id) REFERENCES upf_instances(upf_id) ON DELETE CASCADE
    )`,
	`CREATE INDEX IF NOT EXISTS idx_pfcp_associations_upf_id ON pfcp_associations(upf_id)`,

	// upf_supported_nssai — normalised UPF anchor ↔ S-NSSAI mapping.
	// Replaces the legacy CSV in upf_instances.supported_sst with a
	// proper FK so renames / deletes in nssai_catalog cascade cleanly
	// and runtime SMF UPF selection (TS 23.501 v19.7.0 §6.3.3) can
	// JOIN instead of doing CSV substring matching.
	//
	// Boot-time backfill (engine/schema.go::backfillUPFSupportedNSSAI)
	// parses the legacy CSV for any pre-migration upf_instances rows
	// and populates this table — runtime selection prefers the join
	// when populated, falls back to the CSV otherwise.
	`CREATE TABLE IF NOT EXISTS upf_supported_nssai (
      upf_id          TEXT NOT NULL REFERENCES upf_instances(upf_id) ON DELETE CASCADE,
      nssai_id        INTEGER NOT NULL REFERENCES nssai_catalog(id) ON DELETE CASCADE,
      PRIMARY KEY (upf_id, nssai_id)
    )`,
}

// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
// db/schemas/plmn.go — PLMN support tables.
//
// TS 23.003 §12.1  — PLMN-ID = MCC + MNC
// TS 38.413 §9.2.6.2 — PLMNSupportList (NG Setup Response)
// TS 24.501 §9.11.3.45 — Equivalent PLMN list (Registration Accept)
package schemas

// PLMNDDL seeds the supported_plmns + plmn_nssai + equivalent_plmns tables.
// Registered via schemas.Register from infra/plmn at package init.
var PLMNDDL = []string{
	`CREATE TABLE IF NOT EXISTS supported_plmns (
      plmn_id         TEXT PRIMARY KEY,
      mcc             TEXT NOT NULL,
      mnc             TEXT NOT NULL,
      name            TEXT NOT NULL DEFAULT '',
      plmn_type       TEXT NOT NULL DEFAULT 'home'
                      CHECK (plmn_type IN ('home','equivalent','roaming')),
      amf_region_id   INTEGER,
      amf_set_id      INTEGER,
      amf_pointer     INTEGER,
      mme_group_id    INTEGER,
      mme_code        INTEGER,
      priority        INTEGER NOT NULL DEFAULT 1,
      enabled         INTEGER NOT NULL DEFAULT 1
    )`,

	`CREATE TABLE IF NOT EXISTS plmn_nssai (
      plmn_id         TEXT NOT NULL REFERENCES supported_plmns(plmn_id) ON DELETE CASCADE,
      sst             INTEGER NOT NULL,
      sd              TEXT NOT NULL DEFAULT '',
      -- nssai_id (added in 2026-05; nullable so legacy rows pre-migration
      -- still validate) is the FK index into nssai_catalog.id. New writes
      -- populate it via PLMNAddNSSAI; runtime reads prefer the indexed
      -- nssai_id when set, fall back to (sst,sd) otherwise.
      -- TS 23.501 §5.15.2.1 — S-NSSAI = SST + optional SD.
      nssai_id        INTEGER REFERENCES nssai_catalog(id) ON DELETE CASCADE,
      PRIMARY KEY (plmn_id, sst, sd)
    )`,

	`CREATE TABLE IF NOT EXISTS equivalent_plmns (
      home_plmn_id    TEXT NOT NULL REFERENCES supported_plmns(plmn_id) ON DELETE CASCADE,
      equiv_plmn_id   TEXT NOT NULL REFERENCES supported_plmns(plmn_id) ON DELETE CASCADE,
      PRIMARY KEY (home_plmn_id, equiv_plmn_id)
    )`,
}

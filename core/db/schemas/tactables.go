// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
// db/schemas/tactables.go — Tracking Area + cell/gNB mapping + NSSAI policy.
//
// TS 23.003 §19.4.2.3 — TAI = PLMN + TAC (3 bytes)
// TS 23.501 §5.4.1    — Tracking Area concept
// TS 23.501 §5.4.2    — Registration Area (set of TAIs)
// TS 23.501 §5.15.5.2 — Allowed NSSAI per TA
//
// Named "tactables" to avoid colliding with the existing db/schemas/tcs.go
// (Tactical Communication System) — the filename confuses nothing else.
package schemas

var TrackingAreaDDL = []string{
	`CREATE TABLE IF NOT EXISTS tracking_areas (
      tac             TEXT PRIMARY KEY,
      plmn_mcc        TEXT NOT NULL,
      plmn_mnc        TEXT NOT NULL,
      name            TEXT NOT NULL DEFAULT '',
      paging_priority INTEGER NOT NULL DEFAULT 5
                      CHECK (paging_priority BETWEEN 1 AND 10),
      enabled         INTEGER NOT NULL DEFAULT 1
    )`,

	`CREATE TABLE IF NOT EXISTS ta_cell_map (
      cell_id         TEXT NOT NULL,
      tac             TEXT NOT NULL REFERENCES tracking_areas(tac) ON DELETE CASCADE,
      PRIMARY KEY (cell_id, tac)
    )`,

	`CREATE TABLE IF NOT EXISTS ta_gnb_map (
      gnb_id          TEXT NOT NULL,
      tac             TEXT NOT NULL REFERENCES tracking_areas(tac) ON DELETE CASCADE,
      PRIMARY KEY (gnb_id, tac)
    )`,

	`CREATE TABLE IF NOT EXISTS ta_nssai_policy (
      tac             TEXT NOT NULL REFERENCES tracking_areas(tac) ON DELETE CASCADE,
      sst             INTEGER NOT NULL,
      sd              TEXT,
      allowed         INTEGER NOT NULL DEFAULT 1,
      PRIMARY KEY (tac, sst, sd)
    )`,

	// Registration Areas (TS 23.501 §5.4.2) — a set of TAs
	`CREATE TABLE IF NOT EXISTS registration_areas (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      name            TEXT NOT NULL DEFAULT ''
    )`,

	`CREATE TABLE IF NOT EXISTS registration_area_tas (
      ra_id           INTEGER NOT NULL REFERENCES registration_areas(id) ON DELETE CASCADE,
      tac             TEXT NOT NULL REFERENCES tracking_areas(tac) ON DELETE CASCADE,
      PRIMARY KEY (ra_id, tac)
    )`,

	`CREATE INDEX IF NOT EXISTS idx_ra_tas_tac ON registration_area_tas(tac)`,
}

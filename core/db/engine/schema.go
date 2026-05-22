// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package engine

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/mmt/mmt-studio-core/db/schemas"
)

// strconvParseHex parses a 2-digit zero-padded hex string ("01"…"FF")
// to int. Wrapper kept tiny so engine/schema.go's backfill is self-
// contained.
func strconvParseHex(s string) (int, error) {
	v, err := strconv.ParseInt(s, 16, 0)
	return int(v), err
}

func strconvParseInt(s string) (int, error) {
	v, err := strconv.ParseInt(s, 10, 0)
	return int(v), err
}

// EnsureSchema creates every table in dependency order and runs idempotent
// column-add migrations + singleton bootstrap rows. Does NOT run SeedAll —
// the cold-boot path (webservice/app/app.go::Bootstrap) calls seed.SeedAll
// after this for the operator standalone-GUI flow, while drop-db calls
// EnsureSchema alone so the tester can push its own configuration via
// REST APIs (Full Inversion: tester is the single source of truth for
// runtime DB state).
//
// Safe to call repeatedly — every statement uses CREATE ... IF NOT EXISTS.
func EnsureSchema() error {
	db, err := Open()
	if err != nil {
		return err
	}

	// Foreign keys on for SQLite (modernc sets this via DSN pragma, but be defensive).
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		return fmt.Errorf("enable foreign keys: %w", err)
	}

	for _, stmt := range schemas.GetAllDDL() {
		s := strings.TrimSpace(stmt)
		if s == "" {
			continue
		}
		// Skip CREATE VIEW on first pass — views depend on all tables + may
		// need to be recreated when columns are added.
		if strings.HasPrefix(strings.ToUpper(s), "CREATE VIEW") {
			continue
		}
		if _, err := db.Exec(s); err != nil {
			return fmt.Errorf("DDL failed: %w\nstmt: %s", err, truncate(s, 200))
		}
	}

	// Post-create migrations (idempotent column adds, seeds)
	if err := applyColumnAdditions(db); err != nil {
		return err
	}
	if err := ensureInfraConfigRow(db); err != nil {
		return err
	}
	if err := ensureNetworkConfigRow(db); err != nil {
		return err
	}
	if err := ensureN3iwfConfigRow(db); err != nil {
		return err
	}
	if err := applyV2XAlters(db); err != nil {
		return err
	}
	if err := applyV2XSeed(db); err != nil {
		return err
	}
	if err := applyN3IWFAlters(db); err != nil {
		return err
	}
	if err := migrateNwdafExposureSubsTargetType(db); err != nil {
		return err
	}
	return nil
}

// migrateNwdafExposureSubsTargetType widens the target_type CHECK
// constraint on nwdaf_exposure_subscriptions from
// IN ('imsi','slice','network') to the full TS 23.288 §6.2.2.2
// targetOfAnalyticsReporting set (adds 'nf','nf_set','area').
//
// SQLite cannot ALTER a CHECK constraint in place, so we rename →
// recreate → copy → drop. Idempotent: only runs when the live SQL
// for the table still has the narrow constraint.
func migrateNwdafExposureSubsTargetType(db *sql.DB) error {
	var sqlText string
	err := db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='nwdaf_exposure_subscriptions'`,
	).Scan(&sqlText)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect nwdaf_exposure_subscriptions: %w", err)
	}
	// Already migrated if the new types are present.
	if strings.Contains(sqlText, "'nf'") {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmts := []string{
		`ALTER TABLE nwdaf_exposure_subscriptions RENAME TO nwdaf_exposure_subscriptions_old`,
		`CREATE TABLE nwdaf_exposure_subscriptions (
		   id                INTEGER PRIMARY KEY AUTOINCREMENT,
		   consumer_id       INTEGER NOT NULL REFERENCES nwdaf_exposure_consumers(id) ON DELETE CASCADE,
		   analytics_type    TEXT NOT NULL,
		   target_type       TEXT NOT NULL CHECK(target_type IN ('imsi','slice','network','nf','nf_set','area')),
		   target_id         TEXT,
		   interval_s        INTEGER NOT NULL DEFAULT 60,
		   callback_url      TEXT,
		   active            INTEGER NOT NULL DEFAULT 1,
		   last_notified_at  TEXT,
		   created_at        TEXT NOT NULL DEFAULT (datetime('now'))
		 )`,
		`INSERT INTO nwdaf_exposure_subscriptions
		   (id, consumer_id, analytics_type, target_type, target_id,
		    interval_s, callback_url, active, last_notified_at, created_at)
		 SELECT id, consumer_id, analytics_type, target_type, target_id,
		    interval_s, callback_url, active, last_notified_at, created_at
		 FROM nwdaf_exposure_subscriptions_old`,
		`DROP TABLE nwdaf_exposure_subscriptions_old`,
		`CREATE INDEX IF NOT EXISTS idx_nwdaf_exp_sub_cid ON nwdaf_exposure_subscriptions(consumer_id)`,
		`CREATE INDEX IF NOT EXISTS idx_nwdaf_exp_sub_act ON nwdaf_exposure_subscriptions(active)`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return fmt.Errorf("nwdaf_exposure_subscriptions migrate: %w\nstmt: %s",
				err, truncate(s, 160))
		}
	}
	return tx.Commit()
}

// dataTablesToWipe returns the set of user-data tables that DropAllData
// truncates. Singleton bootstrap tables (network_config, infra_config,
// n3iwf_config — each holds a single id=1 row recreated by EnsureSchema)
// and security_algorithms (CHECK-constrained enum table) stay populated.
//
// Discovered dynamically from sqlite_master so newly-added tables are
// included automatically — the only thing operators need to remember is
// to keep their singletons in the exclusion list below.
func dataTablesToWipe(db *sql.DB) ([]string, error) {
	exclude := map[string]bool{
		"network_config":      true,
		"infra_config":        true,
		"n3iwf_config":        true,
		"sqlite_sequence":     true,
		// security_algorithms is the deployment-wide NEA/NIA catalog
		// (TS 33.501 §5.11) — set once at install time, never varies
		// per test. AMF.InitContextFromDB rejects an empty catalog
		// (returns "security_algorithms table empty") so we must not
		// wipe it on the test-entry drop-db.
		"security_algorithms": true,
	}
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		if exclude[n] {
			continue
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// DropAllData wipes every user-data row but preserves the schema and the
// singleton bootstrap rows (network_config / infra_config / n3iwf_config).
// Used by /api/admin/drop-db-data so the tester can push its own configuration
// without colliding with whatever SeedAll planted on cold boot.
//
// FOREIGN KEYS are turned OFF for the wipe so we don't have to delete in
// dependency order — every DELETE succeeds and ON DELETE CASCADE is
// effectively bypassed (which is what we want: we're erasing everything).
func DropAllData() error {
	db, err := Open()
	if err != nil {
		return err
	}
	tables, err := dataTablesToWipe(db)
	if err != nil {
		return err
	}
	if _, err := db.Exec("PRAGMA foreign_keys=OFF"); err != nil {
		return fmt.Errorf("fk off: %w", err)
	}
	defer db.Exec("PRAGMA foreign_keys=ON")
	for _, t := range tables {
		if _, err := db.Exec("DELETE FROM " + t); err != nil {
			return fmt.Errorf("DELETE FROM %s: %w", t, err)
		}
	}
	// Reset autoincrement counters so the next inserts start from 1 — keeps
	// IDs predictable between test runs.
	_, _ = db.Exec("DELETE FROM sqlite_sequence")
	return nil
}

// applyColumnAdditions runs the ALTER-TABLE-ADD-COLUMN migrations that the
// Python engine performs after CREATE. The column-exists probe is
// SQLite-specific (PRAGMA table_info); extend for PostgreSQL when that
// backend is wired.
func applyColumnAdditions(db *sql.DB) error {
	// ip_version on apn_ip_pools
	if err := ensureColumn(db, "apn_ip_pools", "ip_version", "ip_version INTEGER NOT NULL DEFAULT 4"); err != nil {
		return err
	}
	// sctp_port on network_config — NGAP listener port. Default 38412
	// (TS 38.412 §7 IANA registration). Operator-tunable from the
	// Network Config GUI; existing DBs gain the column with the spec
	// default and continue running unchanged.
	if err := ensureColumn(db, "network_config", "sctp_port",
		"sctp_port INTEGER NOT NULL DEFAULT 38412"); err != nil {
		return err
	}
	// li_auth_token on network_config — gate for /api/li/* per
	// TS 33.127 §5.2 "LI administrative function security". Empty
	// default keeps existing dev DBs runnable; production deployments
	// rotate this from the Network Config GUI / admin tooling.
	if err := ensureColumn(db, "network_config", "li_auth_token",
		"li_auth_token TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// X2/X3 deliverer toggles + poll interval. TS 33.127 §6.3/§6.4
	// reference points are off by default so a fresh deployment
	// doesn't try to push to a non-existent MDF; operators flip the
	// flag once the warrant's mdf_endpoint is configured.
	if err := ensureColumn(db, "network_config", "li_x2_enabled",
		"li_x2_enabled INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := ensureColumn(db, "network_config", "li_x3_enabled",
		"li_x3_enabled INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := ensureColumn(db, "network_config", "li_mdf_poll_interval_ms",
		"li_mdf_poll_interval_ms INTEGER NOT NULL DEFAULT 1000"); err != nil {
		return err
	}
	// Per-row delivery flags on li_cc_sessions for the X3 deliverer.
	// OPENED gets shipped on activation, CLOSED on stop; tracking
	// each independently lets a session that opened+closed in the
	// same tick get both events delivered.
	if err := ensureColumn(db, "li_cc_sessions", "cc_opened_delivered",
		"cc_opened_delivered INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := ensureColumn(db, "li_cc_sessions", "cc_closed_delivered",
		"cc_closed_delivered INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	// nssai_id on plmn_nssai — links per-PLMN advertised slices to the
	// global nssai_catalog (TS 23.501 §5.15.2.1) so renames / deletes
	// cascade. Nullable for transition; backfilled below.
	if err := ensureColumn(db, "plmn_nssai", "nssai_id",
		"nssai_id INTEGER REFERENCES nssai_catalog(id) ON DELETE CASCADE"); err != nil {
		return err
	}
	// Backfill plmn_nssai.nssai_id and upf_supported_nssai from the
	// legacy raw (sst,sd) / supported_sst CSV columns. Idempotent —
	// only fills NULLs / missing rows so re-running is a no-op.
	if err := backfillPlmnNssaiID(db); err != nil {
		return err
	}
	if err := backfillUPFSupportedNSSAI(db); err != nil {
		return err
	}
	// Note: upf_bridge_mode + upf_interface + upf_rest_port were
	// removed when SMF↔UPF went PFCP-only. Existing DBs keep those
	// columns harmlessly (SQLite can't cleanly DROP COLUMN); the
	// CRUD allow-list no longer accepts writes to them.

	// auto_block_ttl_s on security_ids_signatures — when > 0, a
	// signature trip auto-adds the source IP to security_blocked_ips
	// with expires_at = now + ttl. Default 0 keeps existing
	// behaviour for already-deployed signatures.
	if err := ensureColumn(db, "security_ids_signatures", "auto_block_ttl_s",
		"auto_block_ttl_s INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	// expires_at on security_blocked_ips — NULL = permanent. Lazy
	// pruning happens in core_security.IsBlocked / ListBlockedIPs;
	// SweepBlockedIPs() removes the rows on a tick.
	if err := ensureColumn(db, "security_blocked_ips", "expires_at",
		"expires_at TEXT"); err != nil {
		return err
	}
	return nil
}

// backfillPlmnNssaiID walks every plmn_nssai row whose nssai_id is
// still NULL (post-ALTER), finds or creates a matching nssai_catalog
// row by (sst, sd), and writes its id. Only touches NULL rows so this
// is idempotent across re-boots.
//
// nssai_catalog uses UNIQUE(sst, sd) so INSERT OR IGNORE + SELECT id
// yields a stable id whether the row pre-existed (e.g. seeded by
// db/seed/plmn.go) or was created here.
func backfillPlmnNssaiID(db *sql.DB) error {
	rows, err := db.Query(`SELECT plmn_id, sst, sd FROM plmn_nssai WHERE nssai_id IS NULL`)
	if err != nil {
		return err
	}
	type pending struct {
		plmnID string
		sst    int
		sd     string
	}
	var todo []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.plmnID, &p.sst, &p.sd); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, p)
	}
	rows.Close()
	for _, p := range todo {
		// nssai_catalog stores SD as nullable TEXT — the empty PLMN-
		// nssai SD (no SD configured) maps to NULL there per the
		// existing seed pattern, so collapse '' → NULL on lookup.
		var sdArg interface{}
		if p.sd == "" {
			sdArg = nil
		} else {
			sdArg = p.sd
		}
		if _, err := db.Exec(`INSERT OR IGNORE INTO nssai_catalog (sst, sd) VALUES (?, ?)`,
			p.sst, sdArg); err != nil {
			return fmt.Errorf("backfill nssai_catalog: %w", err)
		}
		var id int64
		// IS NULL doesn't match in a normal =, so probe both shapes.
		if p.sd == "" {
			err = db.QueryRow(`SELECT id FROM nssai_catalog WHERE sst=? AND (sd IS NULL OR sd='')`, p.sst).Scan(&id)
		} else {
			err = db.QueryRow(`SELECT id FROM nssai_catalog WHERE sst=? AND sd=?`, p.sst, p.sd).Scan(&id)
		}
		if err != nil {
			return fmt.Errorf("backfill nssai_catalog lookup: %w", err)
		}
		if _, err := db.Exec(`UPDATE plmn_nssai SET nssai_id=? WHERE plmn_id=? AND sst=? AND sd=?`,
			id, p.plmnID, p.sst, p.sd); err != nil {
			return fmt.Errorf("backfill plmn_nssai.nssai_id: %w", err)
		}
	}
	return nil
}

// backfillUPFSupportedNSSAI parses the legacy upf_instances.supported_sst
// CSV column (e.g. "01,02,03") and populates upf_supported_nssai with one
// row per (upf_id, nssai_id). Per-row INSERT OR IGNORE keeps it
// idempotent. SST values are accepted as decimal ("1") or two-digit hex
// ("01") to match the historical free-form CSV the GUI accepted.
func backfillUPFSupportedNSSAI(db *sql.DB) error {
	rows, err := db.Query(`SELECT upf_id, supported_sst FROM upf_instances`)
	if err != nil {
		return err
	}
	type upfRow struct {
		id  string
		csv string
	}
	var upfs []upfRow
	for rows.Next() {
		var u upfRow
		if err := rows.Scan(&u.id, &u.csv); err != nil {
			rows.Close()
			return err
		}
		upfs = append(upfs, u)
	}
	rows.Close()
	for _, u := range upfs {
		// Skip when an explicit join row already exists for this UPF —
		// the operator-curated state always wins over the legacy CSV.
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM upf_supported_nssai WHERE upf_id=?`, u.id).Scan(&n)
		if n > 0 {
			continue
		}
		for _, raw := range strings.Split(u.csv, ",") {
			tok := strings.TrimSpace(raw)
			if tok == "" {
				continue
			}
			sst, ok := parseSSTToken(tok)
			if !ok {
				continue
			}
			// Look up nssai_catalog row by SST alone (PLMN-NSSAI seed
			// uses SD; UPF CSV traditionally has no SD). Take the
			// first match — operators that need SD-specific UPF
			// binding will manage upf_supported_nssai directly.
			var id int64
			if err := db.QueryRow(`SELECT id FROM nssai_catalog WHERE sst=? ORDER BY id LIMIT 1`, sst).Scan(&id); err != nil {
				// No matching slice yet — create a bare entry so the
				// CSV value isn't silently dropped; admin can rename
				// it later in the Slice Catalog tab.
				if _, e2 := db.Exec(`INSERT OR IGNORE INTO nssai_catalog (sst) VALUES (?)`, sst); e2 != nil {
					return fmt.Errorf("backfill nssai_catalog from upf csv: %w", e2)
				}
				if e3 := db.QueryRow(`SELECT id FROM nssai_catalog WHERE sst=? ORDER BY id LIMIT 1`, sst).Scan(&id); e3 != nil {
					return fmt.Errorf("backfill nssai_catalog re-lookup: %w", e3)
				}
			}
			if _, err := db.Exec(`INSERT OR IGNORE INTO upf_supported_nssai (upf_id, nssai_id) VALUES (?, ?)`,
				u.id, id); err != nil {
				return fmt.Errorf("backfill upf_supported_nssai: %w", err)
			}
		}
	}
	return nil
}

// parseSSTToken accepts SST as decimal ("1") or two-digit hex ("01"),
// returning the integer SST value and true on success. Existing CSVs
// in the wild use both shapes interchangeably.
func parseSSTToken(s string) (int, bool) {
	// Two-digit zero-padded hex per TS 24.501 §9.11.2.8 wire format.
	if len(s) == 2 {
		if v, err := strconvParseHex(s); err == nil {
			return v, true
		}
	}
	if v, err := strconvParseInt(s); err == nil {
		return v, true
	}
	return 0, false
}

func applyV2XAlters(db *sql.DB) error {
	for _, a := range schemas.V2XAlterUE {
		if err := ensureColumn(db, "ue", a.Column, a.DDL); err != nil {
			return err
		}
	}
	return nil
}

// applyN3IWFAlters adds the AMF connectivity columns the original
// CREATE TABLE didn't carry. Empty defaults keep existing rows valid
// — Start() treats amf_addr=="" as "no N2 bridge" and brings up IKE
// without bridging to AMF.
func applyN3IWFAlters(db *sql.DB) error {
	for _, a := range schemas.N3IWFAlter {
		if err := ensureColumn(db, "n3iwf_config", a.Column, a.DDL); err != nil {
			return err
		}
	}
	return nil
}

func applyV2XSeed(db *sql.DB) error {
	for _, s := range schemas.V2XSeed {
		if _, err := db.Exec(s); err != nil {
			return fmt.Errorf("V2X seed: %w\nstmt: %s", err, truncate(s, 160))
		}
	}
	return nil
}

func ensureInfraConfigRow(db *sql.DB) error {
	_, err := db.Exec(`INSERT OR IGNORE INTO infra_config (id) VALUES (1)`)
	return err
}

func ensureNetworkConfigRow(db *sql.DB) error {
	_, err := db.Exec(`INSERT OR IGNORE INTO network_config (id) VALUES (1)`)
	return err
}

func ensureN3iwfConfigRow(db *sql.DB) error {
	_, err := db.Exec(`INSERT OR IGNORE INTO n3iwf_config (id) VALUES (1)`)
	return err
}

// ensureColumn adds a column if it doesn't already exist (SQLite-specific probe).
func ensureColumn(db *sql.DB, table, col, ddl string) error {
	exists, err := columnExists(db, table, col)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	if _, err := db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", table, ddl)); err != nil {
		return fmt.Errorf("add column %s.%s: %w", table, col, err)
	}
	return nil
}

func columnExists(db *sql.DB, table, col string) (bool, error) {
	if DBType == "postgresql" {
		var n int
		err := db.QueryRow(
			`SELECT 1 FROM information_schema.columns WHERE table_name=$1 AND column_name=$2`,
			table, col).Scan(&n)
		if err == sql.ErrNoRows {
			return false, nil
		}
		return err == nil, err
	}
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == col {
			return true, nil
		}
	}
	return false, rows.Err()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

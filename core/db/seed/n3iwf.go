// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
// db/seed/n3iwf.go — N3IWF single-row config defaults.
package seed

import "database/sql"

// SeedN3IWFConfig guarantees the n3iwf_config singleton row exists. Idempotent.
func SeedN3IWFConfig(db *sql.DB) error {
	_, err := db.Exec(`INSERT OR IGNORE INTO n3iwf_config (id) VALUES (1)`)
	return err
}

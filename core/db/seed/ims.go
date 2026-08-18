// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
// db/seed/ims.go — Default IMS subscriber provisioning (TS 23.003 §13.3/§13.4).
package seed

import (
	"database/sql"
	"fmt"
)

// SeedIMSSubscribers mirrors the Python helper: creates IMPI/IMPU rows for
// every already-provisioned UE in the range 001011234560001-128 (matches
// the baseline.yaml roster). Skips UEs that don't exist (caller is
// responsible for calling SeedDefaultUEs first when IMS subscribers are
// wanted on a blank DB).
//
// IMSDomain is configurable so ND-style labs can override mnc001.mcc001.
func SeedIMSSubscribers(db *sql.DB, imsDomain string) error {
	if imsDomain == "" {
		imsDomain = "ims.mnc001.mcc001.3gppnetwork.org"
	}
	if _, err := db.Exec(
		`INSERT OR IGNORE INTO ims_service_profiles (name, filter_criteria_json) VALUES ('default_profile', '[]')`,
	); err != nil {
		return err
	}
	var spID int64
	if err := db.QueryRow(
		`SELECT id FROM ims_service_profiles WHERE name='default_profile'`).Scan(&spID); err != nil {
		return err
	}
	for i := 1; i <= 128; i++ {
		imsi := fmt.Sprintf("00101123456%04d", i)
		impi := fmt.Sprintf("%s@%s", imsi, imsDomain)
		impu := fmt.Sprintf("sip:%s@%s", imsi, imsDomain)

		var exists int
		if err := db.QueryRow(`SELECT 1 FROM ims_subscribers WHERE impi=?`, impi).Scan(&exists); err == nil {
			continue
		}
		var ueID int64
		if err := db.QueryRow(`SELECT id FROM ue WHERE imsi=?`, imsi).Scan(&ueID); err != nil {
			continue // UE not provisioned yet
		}
		if _, err := db.Exec(
			`INSERT INTO ims_subscribers (ue_id, impi, impu, service_profile_id) VALUES (?,?,?,?)`,
			ueID, impi, impu, spID,
		); err != nil {
			return err
		}
	}
	return nil
}

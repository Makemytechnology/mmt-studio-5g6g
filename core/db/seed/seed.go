// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package seed — default data per domain.
//
// Go port of db/seed/. Tables start empty — operators configure PLMN,
// TAC, APN, slices, and UEs from the GUI. Seed functions only provide
// the minimum-viable defaults (network_config singleton, security
// algorithms, standardized QoS profiles, charging profiles, …).
//
// SeedAll is invoked from engine.EnsureSchema at boot, same as the
// Python reference. Individual seeders are also exported so tests /
// migrations can run a subset in isolation.
package seed

import "database/sql"

// SeedAll runs every seeder in dependency order. Each step is idempotent
// and failure-tolerant (a missing optional table is skipped with a nil
// error) to mirror the try/except guards in db/seed/__init__.py.
func SeedAll(db *sql.DB) error {
	if err := SeedNetworkConfig(db); err != nil {
		return err
	}
	if err := SeedN3IWFConfig(db); err != nil {
		return err
	}
	if err := SeedChargingProfiles(db); err != nil {
		return err
	}
	if err := SeedServices(db); err != nil {
		return err
	}
	if err := SeedPLMN(db); err != nil {
		return err
	}
	if err := SeedDefaultSubscriber(db); err != nil {
		return err
	}
	// Baseline UE roster — 128 deterministic UEs across 3 slices, matches
	// db/seed/baseline.yaml. Idempotent: ue.go skips IMSIs that already
	// exist, so re-running across upgrades is safe.
	if err := SeedDefaultUEs(db); err != nil {
		return err
	}
	if err := SeedUPF(db); err != nil {
		return err
	}
	// V2X seed is applied from engine.applyV2XSeed (it sits next to the
	// schema ALTER TABLE statements, so the two stay in lockstep).
	return nil
}

// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
// db/crud/ta_policy.go — per-TA NSSAI policy lookup (TS 23.501 §5.15.3).
package crud

import (
	"github.com/mmt/mmt-studio-core/db/engine"
)

// TANssaiPolicyAllows returns whether (sst, sd) is allowed in the
// given TAC per the operator's ta_nssai_policy table (TS 23.501
// §5.15.3 / §5.15.5.2 — per-TA slice support).
//
// Semantics: missing row → default-allow. The schema's `allowed`
// column lets operators explicitly *deny* a slice in a TA (allowed=0)
// or explicitly allow (allowed=1, default). SD matching uses the
// IFNULL('','') equivalence so a stored NULL/empty SD matches any
// query SD (wildcard per TS 23.003 §28.4.2).
func TANssaiPolicyAllows(tac string, sst int, sd string) bool {
	if tac == "" {
		return true
	}
	db, err := engine.Open()
	if err != nil {
		return true
	}
	var allowed int
	err = db.QueryRow(
		`SELECT allowed FROM ta_nssai_policy
		 WHERE tac=? AND sst=? AND IFNULL(sd,'')=IFNULL(?, '')`,
		tac, sst, nullIfEmpty(sd),
	).Scan(&allowed)
	if err != nil {
		return true
	}
	return allowed == 1
}

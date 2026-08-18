// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package schemas combines per-domain DDL into a single ordered list for
// ensure_schema(). Port of db/schemas/__init__.py.
//
// Domain modules expose their own DDL slice (CoreDDL, NetworkDDL, …) so
// subsystems can own their schema alongside their code. Future domains
// (NWDAF, MCX, eSIM, IoT, TAC, PLMN, …) register themselves via Register().
package schemas

import "sync"

var (
	extraMu sync.Mutex
	extras  []namedDDL
)

type namedDDL struct {
	Name string
	DDL  []string
}

// Register adds a domain DDL slice to be included by GetAllDDL.
// Call from an init() in the domain package, e.g.:
//
//	func init() { schemas.Register("nwdaf", NWDAF_DDL) }
func Register(name string, ddl []string) {
	extraMu.Lock()
	defer extraMu.Unlock()
	extras = append(extras, namedDDL{Name: name, DDL: ddl})
}

// GetAllDDL returns every DDL statement in dependency order:
//
//  1. core   (ue, auth, subscription, services, bindings)
//  2. network (network_config, apn, slices, security)
//  3. ims    (service_profiles, dialogs)
//  4. billing (charging_profiles, tariff_plans, CDRs, balances, invoices)
//  5. fm     (alarms)
//  6. n3iwf  (n3iwf_config)
//  7. infra  (infrastructure single-row config)
//  8. logger (per-module logger config)
//  9. positioning (LMF / GMLC)
// 10. tcs    (tactical node sync)
// 11. v2x    (PQI catalog + v2x_config)
// 12. ...    (anything registered via Register)
//
// Order matters: billing must precede core because core references
// charging_profiles(name) via FK. Same rule as the Python reference.
func GetAllDDL() []string {
	ddl := make([]string, 0, 256)
	ddl = append(ddl, BillingDDL...)      // charging_profiles first (FK target)
	ddl = append(ddl, NetworkDDL...)      // apn_config before core (FK target)
	ddl = append(ddl, CoreDDL...)         // ue + subscription tree
	ddl = append(ddl, IMSDDL...)          // ims_service_profiles, ims_dialogs
	ddl = append(ddl, FMDDL...)           // alarms
	ddl = append(ddl, N3IWFDDL...)        // n3iwf_config
	ddl = append(ddl, InfraDDL...)        // infra_config
	ddl = append(ddl, LoggerDDL...)       // logger_config
	ddl = append(ddl, PositioningDDL...)  // positioning_sessions
	ddl = append(ddl, TCSDDL...)          // tactical sync
	ddl = append(ddl, V2XDDL...)          // v2x_service_types
	ddl = append(ddl, PLMNDDL...)         // supported_plmns / plmn_nssai / equivalent
	ddl = append(ddl, TrackingAreaDDL...) // tracking_areas / ta_cell_map / ta_gnb_map / ta_nssai_policy
	ddl = append(ddl, SMFDDL...)          // upf_instances / pfcp_associations
	extraMu.Lock()
	for _, e := range extras {
		ddl = append(ddl, e.DDL...)
	}
	extraMu.Unlock()
	return ddl
}

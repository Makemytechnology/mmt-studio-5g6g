// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Domain REST routes — IMS, MCX, eSIM, IoT, DPI, positioning, SMSF, edge
// (EAS/MEC/ISAC/Ranging/TSN), safety (Emergency/IOPS/MBS/PWS/RACS/DR),
// security (NPN/RAN-sharing), and infrastructure (infra-config-history,
// TAC CRUD, PLMN CRUD).
//
// Port of webservice/routes/*.py. Each endpoint returns the shape the
// corresponding HTML panel expects so the existing templates work without
// any frontend change.
//
// Many of these panels are read-heavy and draw from the DB tables created
// by the per-domain schema files. The write paths (POST/DELETE) that mutate
// domain-specific state live alongside the relevant NF package (e.g.
// nf/chf, services/ims, …) and are called from here.
package app

import (
	"net/http"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// RegisterDomainRoutes wires every remaining GUI panel API.
func (s *Server) RegisterDomainRoutes() {
	s.registerIMSRoutes()
	s.registerMiscRoutes()
	s.registerPositioningRoutes()
	s.registerRangingRoutes()
	s.registerISACRoutes()
	s.registerSMSFRoutes()
	s.registerN3IWFRoutes()
	s.registerMECRoutes()
	s.registerEdgeRoutes()
	s.registerTACRoutes()
	s.registerBillingRoutes()
	s.registerPLMNRoutes()
	s.registerNSaaSRoutes()
	s.registerV2XRoutes()
	s.registerUASRoutes()
	s.registerProSeRoutes()
	s.registerSEALRoutes()
	s.registerWiFiOffloadRoutes()
	s.registerNTNRoutes()
	s.registerN26Routes()
	s.registerRoamingRoutes()
	s.registerRANSharingRoutes()
	s.registerEmergencyRoutes()
	s.registerPWSRoutes()
	s.registerIOPSRoutes()
	s.registerDisasterRoamingRoutes()
	s.registerMBSRoutes()
	s.registerRACSRoutes()
	s.registerKPIsRoutes()
	s.registerFMRoutes()
	s.registerTraceRoutes()
	s.registerNWDAFAnalyticsRoutes()
	s.registerNWDAFExposureRoutes()
	s.registerSecurityRoutes()
	s.registerNPNRoutes()
	s.registerSEPPRoutes()
	s.registerLIRoutes()
	s.registerOTELRoutes()
	s.registerUSSDRoutes()
	s.registerSupplementaryRoutes()
	s.registerCHFRoutes()
	s.registerURSPRoutes()
	s.registerPCFRoutes()
	s.registerESIMRoutes()
	s.registerMUSIMRoutes()
}

// ── Generic helpers ─────────────────────────────────────────────────────

// dbListRoute runs a fixed SELECT and returns the rows as JSON array of maps.
// Used for read-only panels that directly mirror a table.
func dbListRoute(query string) http.HandlerFunc {
	return func(w http.ResponseWriter, rq *http.Request) {
		db, err := engine.Open()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		rows, err := db.Query(query)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		cols, _ := rows.Columns()
		var out []map[string]any
		for rows.Next() {
			scan := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range scan {
				ptrs[i] = &scan[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				continue
			}
			row := make(map[string]any, len(cols))
			for i, name := range cols {
				row[name] = scan[i]
			}
			out = append(out, row)
		}
		if out == nil {
			out = []map[string]any{}
		}
		jsonReply(w, out)
	}
}

// emptyArrayRoute returns [] — used for panels whose backing NF hasn't
// landed yet so the fetch() doesn't 404.
func emptyArrayRoute(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte("[]"))
}

// emptyObjRoute returns {} — for single-object dashboard panels.
func emptyObjRoute(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte("{}"))
}

// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Auto-extracted from domain_routes.go (refactor: split god function by
// domain banner). Do not re-merge — keep new domain APIs in their own
// routes_<domain>.go file.
package app

import (
	"net/http"

	"github.com/mmt/mmt-studio-core/db/crud"
	"github.com/mmt/mmt-studio-core/nf/smf/ipalloc"
	"github.com/mmt/mmt-studio-core/nf/smf/upf"
	upfpfcp "github.com/mmt/mmt-studio-core/nf/upf/pfcp"
	"github.com/mmt/mmt-studio-core/oam/platform"
	"github.com/mmt/mmt-studio-core/oam/trace"
	"github.com/mmt/mmt-studio-core/security/dpi"
)

func (s *Server) registerMiscRoutes() {
	r := s.Router

	// ── MCX ──────────────────────────────────────────────────────────
	r.Get("/api/mcx/users", emptyArrayRoute)
	r.Get("/api/mcx/groups", emptyArrayRoute)
	r.Get("/api/mcx/calls", emptyArrayRoute)
	r.Get("/api/mcx/messages", emptyArrayRoute)

	// /api/esim/* lives in routes_esim.go (registered via
	// RegisterDomainRoutes; wired to services/esim + services/esim/smdp —
	// GSMA SGP.22 §3 ES2+/ES9+).

	// ── IoT ──────────────────────────────────────────────────────────
	r.Get("/api/iot/dashboard", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{
			"redcap_devices": 0,
			"nbiot_devices":  0,
			"ambient_tags":   0,
		})
	})
	r.Get("/api/iot/nbiot/psm", emptyArrayRoute)
	r.Get("/api/iot/nbiot/cp-data", emptyArrayRoute)
	r.Get("/api/iot/tags", emptyArrayRoute)
	r.Get("/api/iot/readers", emptyArrayRoute)

	// ── DPI ── TS 23.501 §5.8.2.4 (classifiers), §5.8.2.6 (charging)
	r.Get("/api/dpi/apps", func(w http.ResponseWriter, rq *http.Request) {
		apps, err := dpi.ListApps()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if apps == nil {
			apps = []map[string]interface{}{}
		}
		jsonReply(w, map[string]any{"items": apps})
	})
	r.Get("/api/dpi/rules", func(w http.ResponseWriter, rq *http.Request) {
		// Optional ?app_id= filter.
		appID := rq.URL.Query().Get("app_id")
		rules, err := dpi.GetPFDRules(appID)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if rules == nil {
			rules = []map[string]interface{}{}
		}
		jsonReply(w, map[string]any{"items": rules})
	})
	// /api/dpi/upf-pfd-state — read-only view of the UP function's
	// PFD cache after each TS 29.244 §6.2.5 PFD-Management push.
	// Operator OAM uses this to verify the SMF→UPF wire took effect.
	r.Get("/api/dpi/upf-pfd-state", func(w http.ResponseWriter, rq *http.Request) {
		cache := upfpfcp.GetPFDCache()
		apps, rules := upfpfcp.PFDCacheStats()
		jsonReply(w, map[string]any{
			"app_count":  apps,
			"rule_count": rules,
			"cache":      cache,
		})
	})
	r.Get("/api/dpi/usage-summary", func(w http.ResponseWriter, rq *http.Request) {
		summary, err := dpi.GetAppUsageSummary()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var total int64
		for _, row := range summary {
			if v, ok := row["total_bytes"].(int64); ok {
				total += v
			}
		}
		if summary == nil {
			summary = []map[string]interface{}{}
		}
		jsonReply(w, map[string]any{"total_bytes": total, "apps": summary})
	})

	// ── Traces (TS 32.422) ───────────────────────────────────────────
	r.Get("/api/traces", func(w http.ResponseWriter, rq *http.Request) {
		records, _ := trace.ListRecords(200)
		if records == nil {
			records = []map[string]any{}
		}
		jsonReply(w, records)
	})

	// ── Platform Info ────────────────────────────────────────────────
	r.Get("/api/platform", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, platform.Get())
	})

	// ── Edge ─────────────────────────────────────────────────────────
	// Full /api/eas/* surface lives in routes_edge.go (registry,
	// discover, dnai, dns, discovery-log).

	// ── Safety ───────────────────────────────────────────────────────
	// Emergency, IOPS, MBS, PWS, RACS, and Disaster-Roaming routes
	// live in routes_emergency.go / routes_iops.go / routes_mbs.go /
	// routes_pws.go / routes_racs.go / routes_disaster_roaming.go
	// (all wired to safety/<package>).

	// /api/security/* lives in routes_security.go (registered via
	// RegisterDomainRoutes; wired to security/core_security).

	// ── UPF registry ─────────────────────────────────────────────────
	r.Get("/api/upf/instances", func(w http.ResponseWriter, rq *http.Request) {
		list, err := upf.List()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, list)
	})

	// ── IP pool usage ────────────────────────────────────────────────
	r.Get("/api/ip-pool-usage", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, ipalloc.Default.Usage())
	})

	// ── Infra config history ─────────────────────────────────────────
	r.Get("/api/infra-config-history", func(w http.ResponseWriter, rq *http.Request) {
		hist, err := crud.ListInfraHistory()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, hist)
	})
}

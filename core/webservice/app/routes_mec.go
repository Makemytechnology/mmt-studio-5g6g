// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_mec.go — REST surface for the MEC orchestrator
// (edge/mec/mec.go).
//
// 5GC anchors:
//
//   - TS 23.501 §5.6.5 LADN — sites carry a TAI list which is the LADN
//     service area (UE attached in any of those TAIs is in scope).
//   - TS 23.501 §5.13 Edge Computing — the umbrella; sites + apps +
//     traffic rules together compose the operator-facing edge surface.
//   - TS 23.502 §4.3.6 AF traffic-routing influence — /api/mec/af-
//     influence creates a traffic rule that the SMF consumes when
//     programming the PSA-UPF / ULCL towards the edge target.
//   - TS 23.548 §6.2.3.2.2 EASDF — FQDN→app lookup helper exposed for
//     the SMF / EASDF stub to drive Session-Breakout discovery.
//   - TS 23.558 §8.12 Dynamic EAS instantiation triggering — the
//     deploy/undeploy primitives are the orchestrator-side persistence
//     of that procedure's outcome.
//
// Wire format follows the JSON shape the OAM panel + tester expect.
// A future TS 29.558-conformant translator could sit in front of
// these routes without touching the data path.

package app

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/mmt/mmt-studio-core/edge/mec"
	"github.com/mmt/mmt-studio-core/nf/smf/session"
)

func (s *Server) registerMECRoutes() {
	r := s.Router

	// ── Status (TS 23.501 §5.13 umbrella view) ─────────────────────
	r.Get("/api/mec/status", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, mec.Status())
	})

	// ── Sites (TS 23.501 §5.6.5 LADN service areas) ────────────────
	r.Get("/api/mec/sites", func(w http.ResponseWriter, rq *http.Request) {
		sites := mec.ListSites()
		if sites == nil {
			sites = []*mec.Site{}
		}
		jsonReply(w, map[string]any{"sites": sites})
	})
	r.Post("/api/mec/sites", func(w http.ResponseWriter, rq *http.Request) {
		var b struct {
			Name        string   `json:"name"`
			TAIs        []string `json:"tais"`
			LocalDNIP   string   `json:"local_dn_ip"`
			LocalDNCIDR string   `json:"local_dn_cidr"`
			Capacity    int      `json:"capacity"`
		}
		if err := json.NewDecoder(rq.Body).Decode(&b); err != nil {
			jsonError(w, "invalid json", http.StatusBadRequest)
			return
		}
		if b.Name == "" {
			jsonError(w, "name required", http.StatusBadRequest)
			return
		}
		site := mec.AddSite(b.Name, b.TAIs, b.LocalDNIP, b.LocalDNCIDR, b.Capacity)
		jsonReply(w, map[string]any{"ok": true, "site": site})
	})
	r.Delete("/api/mec/sites/{site_id}", func(w http.ResponseWriter, rq *http.Request) {
		ok := mec.RemoveSite(chi.URLParam(rq, "site_id"))
		jsonReply(w, map[string]any{"ok": ok})
	})

	// ── Apps (TS 23.558 §8.4.3 EAS registration view) ──────────────
	r.Get("/api/mec/apps", func(w http.ResponseWriter, rq *http.Request) {
		list := mec.ListApps()
		if list == nil {
			list = []*mec.App{}
		}
		jsonReply(w, map[string]any{"apps": list})
	})
	r.Post("/api/mec/apps", func(w http.ResponseWriter, rq *http.Request) {
		var b struct {
			Name     string `json:"name"`
			FQDN     string `json:"fqdn"`
			DNN      string `json:"dnn"`
			IPFilter string `json:"ip_filter"`
			Port     int    `json:"port"`
			Protocol string `json:"protocol"`
		}
		if err := json.NewDecoder(rq.Body).Decode(&b); err != nil {
			jsonError(w, "invalid json", http.StatusBadRequest)
			return
		}
		if b.Name == "" {
			jsonError(w, "name required", http.StatusBadRequest)
			return
		}
		a := mec.AddApp(b.Name, b.FQDN, b.DNN, b.IPFilter, b.Port, b.Protocol)
		jsonReply(w, map[string]any{"ok": true, "app": a})
	})
	r.Delete("/api/mec/apps/{app_id}", func(w http.ResponseWriter, rq *http.Request) {
		ok := mec.RemoveApp(chi.URLParam(rq, "app_id"))
		jsonReply(w, map[string]any{"ok": ok})
	})

	// ── Deploy / undeploy (TS 23.558 §8.12 dynamic EAS) ────────────
	r.Post("/api/mec/deploy", func(w http.ResponseWriter, rq *http.Request) {
		var b struct {
			AppID   string `json:"app_id"`
			SiteID  string `json:"site_id"`
			AppIP   string `json:"app_ip"`
			AppPort int    `json:"app_port"`
		}
		if err := json.NewDecoder(rq.Body).Decode(&b); err != nil {
			jsonError(w, "invalid json", http.StatusBadRequest)
			return
		}
		inst, err := mec.DeployInstance(b.AppID, b.SiteID, b.AppIP, b.AppPort)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "instance": inst})
	})
	r.Post("/api/mec/undeploy", func(w http.ResponseWriter, rq *http.Request) {
		var b struct {
			AppID  string `json:"app_id"`
			SiteID string `json:"site_id"`
		}
		if err := json.NewDecoder(rq.Body).Decode(&b); err != nil {
			jsonError(w, "invalid json", http.StatusBadRequest)
			return
		}
		ok := mec.UndeployInstance(b.AppID, b.SiteID)
		jsonReply(w, map[string]any{"ok": ok})
	})

	// ── Traffic rules (TS 23.502 §4.3.6 AF-influence read view) ────
	r.Get("/api/mec/ulcl-rules", func(w http.ResponseWriter, rq *http.Request) {
		rules := mec.ListTrafficRules()
		if rules == nil {
			rules = []*mec.TrafficRule{}
		}
		jsonReply(w, map[string]any{"rules": rules})
	})

	// ── AF-influence write surface (TS 23.502 §4.3.6 / TS 29.522
	//    Nnef_TrafficInfluence). The local stack persists the rule
	//    in-memory; the SMF consults the rule list when building the
	//    PSA-UPF FAR (ULCL/BP) on the next PDU session establish for
	//    a matching DNN. A future Nnef_TrafficInfluence_Create wire
	//    sits in front of this same handler.
	r.Post("/api/mec/af-influence", func(w http.ResponseWriter, rq *http.Request) {
		var b struct {
			AppID      string `json:"app_id"`
			SiteID     string `json:"site_id"`
			DNN        string `json:"dnn"`
			TargetIP   string `json:"target_ip"`
			TargetFQDN string `json:"target_fqdn"`
			TargetPort int    `json:"target_port"`
			Priority   int    `json:"priority"`
		}
		if err := json.NewDecoder(rq.Body).Decode(&b); err != nil {
			jsonError(w, "invalid json", http.StatusBadRequest)
			return
		}
		rule := mec.AddTrafficRule(b.AppID, b.SiteID, b.DNN, b.TargetIP,
			b.TargetFQDN, b.TargetPort, b.Priority)
		jsonReply(w, map[string]any{"ok": true, "rule": rule, "req_id": rule.RuleID})
	})
	r.Get("/api/mec/af-influences", func(w http.ResponseWriter, rq *http.Request) {
		rules := mec.ListTrafficRules()
		if rules == nil {
			rules = []*mec.TrafficRule{}
		}
		jsonReply(w, map[string]any{"influences": rules})
	})
	r.Delete("/api/mec/af-influence/{rule_id}", func(w http.ResponseWriter, rq *http.Request) {
		ok := mec.DeleteTrafficRule(chi.URLParam(rq, "rule_id"))
		jsonReply(w, map[string]any{"ok": ok})
	})

	// ── Active sessions × AF-influence (TS 23.502 §4.3.6 OAM view).
	//    Joins smf.session.Default.All() with mec.RulesForDNN to show
	//    which currently-active PDU sessions are subject to operator-
	//    authored edge steering rules. Read-only, no mutation.
	r.Get("/api/mec/active-sessions", func(w http.ResponseWriter, rq *http.Request) {
		all := session.Default.All()
		out := make([]map[string]any, 0, len(all))
		for _, s := range all {
			rules := mec.RulesForDNN(s.DNN)
			ulcl := mec.ULCLForSession(s.IMSI, int(s.PDUSessionID))
			installed := 0
			for _, st := range ulcl {
				if st.Installed {
					installed++
				}
			}
			out = append(out, map[string]any{
				"imsi":               s.IMSI,
				"pdu_session_id":     s.PDUSessionID,
				"dnn":                s.DNN,
				"upf_id":             s.UPFID,
				"af_rule_count":      len(rules),
				"af_rules":           rules,
				"ulcl_state":         ulcl,
				"ulcl_installed":     installed,
				"ulcl_attempted":     len(ulcl),
			})
		}
		jsonReply(w, map[string]any{"sessions": out, "count": len(out)})
	})

	// ── EASDF lookup helper (TS 23.548 §6.2.3.2.2) ─────────────────
	// Read-only convenience for SMF / EASDF integrations.
	r.Get("/api/mec/lookup", func(w http.ResponseWriter, rq *http.Request) {
		fqdn := rq.URL.Query().Get("fqdn")
		tai := rq.URL.Query().Get("tai")
		appID := rq.URL.Query().Get("app_id")
		out := map[string]any{}
		if fqdn != "" {
			if a := mec.FindAppByFQDN(fqdn); a != nil {
				out["app"] = a
			}
		}
		if tai != "" {
			if site := mec.FindSiteByTAI(tai); site != nil {
				out["site"] = site
			}
		}
		if appID != "" && tai != "" {
			if inst := mec.FindNearestInstance(appID, tai); inst != nil {
				out["instance"] = inst
			}
		}
		jsonReply(w, out)
	})
}

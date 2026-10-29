// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package app

import (
	"net/http"

	"github.com/flosch/pongo2/v6"
)

// RegisterRoutes wires the baseline HTML pages. Mirrors the landing-page
// routes in webservice/app.py; per-domain API routers (AMF/SMF/IMS/…) plug
// into the same chi.Mux via server.Router.Mount("/api/...", handler).
func (s *Server) RegisterRoutes() {
	r := s.Router

	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		s.Render(w, r, "index.html", pongo2.Context{
			"title": "MMT Studio Core",
		})
	})

	// Each standalone HTML panel gets a one-liner GET. When per-page
	// backend routes (AJAX endpoints) land, they register under /api/
	// alongside their template.
	pages := []struct {
		path, tmpl, title string
	}{
		{"/health", "health.html", "Health"},
		{"/faults", "faults.html", "Faults"},
		{"/logger", "logger.html", "Logger"},
		{"/traces", "traces.html", "Traces"},
		{"/kpis", "kpis.html", "KPIs"},
		{"/contexts", "contexts.html", "Contexts"},
		{"/slices", "slices.html", "Slices"},
		{"/apn", "apn.html", "APN/DNN"},
		{"/infrastructure", "infrastructure.html", "Infrastructure"},
		{"/network-config", "network_config.html", "Network Config"},
		{"/plmn", "plmn.html", "PLMN"},
		{"/tac", "tac.html", "TAC"},
		{"/security", "security.html", "Security"},
		{"/services", "services.html", "Services"},
		{"/ims", "ims.html", "IMS"},
		{"/mcx", "mcx.html", "MCX"},
		{"/billing", "billing.html", "Billing"},
		{"/chf", "chf.html", "CHF"},
		{"/charging-profiles", "charging_profiles.html", "Charging Profiles"},
		{"/positioning", "positioning.html", "Positioning"},
		{"/roaming", "roaming.html", "Roaming"},
		{"/n26", "n26.html", "N26"},
		{"/n3iwf", "n3iwf.html", "N3IWF"},
		{"/ntn", "ntn.html", "NTN"},
		{"/ntn-phase2", "ntn_phase2.html", "NTN Phase 2"},
		{"/iot", "iot.html", "IoT"},
		{"/nidd", "nidd.html", "NIDD"},
		{"/mec", "mec.html", "MEC"},
		{"/eas", "eas.html", "EAS"},
		{"/isac", "isac.html", "ISAC"},
		{"/ranging", "ranging.html", "Ranging"},
		{"/esim", "esim.html", "eSIM"},
		{"/nwdaf", "nwdaf.html", "NWDAF"},
		{"/nwdaf-exposure", "nwdaf_exposure.html", "NWDAF Exposure"},
		{"/nsacf", "nsacf.html", "NSACF"},
		{"/nsaas", "nsaas.html", "NSaaS"},
		{"/npn", "npn.html", "NPN"},
		{"/pin", "pin.html", "PIN"},
		{"/prose", "prose.html", "ProSe"},
		{"/seal", "seal.html", "SEAL"},
		{"/dpi", "dpi.html", "DPI"},
		{"/li", "li.html", "LI"},
		{"/smsf", "smsf.html", "SMSF"},
		{"/supplementary", "supplementary.html", "Supplementary"},
		{"/musim", "musim.html", "Multi-USIM"},
		{"/ran-sharing", "ran_sharing.html", "RAN Sharing"},
		{"/resilience", "resilience.html", "Resilience"},
		{"/emergency", "emergency.html", "Emergency"},
		{"/iops", "iops.html", "IOPS"},
		{"/mbs", "mbs.html", "MBS"},
		{"/pws", "pws.html", "PWS"},
		{"/racs", "racs.html", "RACS"},
		{"/disaster-roaming", "disaster_roaming.html", "Disaster Roaming"},
		{"/tcs", "tcs.html", "TCS"},
		{"/traffic", "traffic.html", "Traffic"},
		{"/benchmark", "benchmark.html", "Benchmark"},
		{"/utils", "utils.html", "Utilities"},
		{"/ursp", "ursp.html", "URSP"},
		{"/tsn", "tsn.html", "TSN"},
		{"/ai-assistant", "ai_assistant.html", "AI Assistant"},
		{"/uas", "uas.html", "UAS"},
		{"/ue-dashboard", "ue_dashboard.html", "UE Dashboard"},
		{"/ue-auth", "ue_auth_info.html", "UE Auth"},
		{"/ue-subscription", "ue_subscription.html", "UE Subscription"},
		{"/ue-config", "ue_config.html", "UE Config"},
	}
	for _, p := range pages {
		p := p
		r.Get(p.path, func(w http.ResponseWriter, rq *http.Request) {
			s.Render(w, rq, p.tmpl, pongo2.Context{"title": p.title})
		})
	}
}

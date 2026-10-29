// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Auto-extracted from domain_routes.go (refactor: split god function by
// domain banner). Do not re-merge — keep new domain APIs in their own
// routes_<domain>.go file.
package app

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/mmt/mmt-studio-core/iot/nidd"
	"github.com/mmt/mmt-studio-core/nf/smf/upfclient"
	"github.com/mmt/mmt-studio-core/security/dpi"
	"github.com/mmt/mmt-studio-core/services/nsaas"
)

// pushDPIToUPF pulls the current PFD set from security/dpi and ships
// it to every registered UPF anchor over the §6.2.5 PFCP PFD-
// Management procedure. Best-effort: errors are logged but never
// fail the operator HTTP call (the local DB cache is the canonical
// source — the wire push is a sync, not an authoritative store).
func pushDPIToUPF() {
	if upfclient.DefaultRouter == nil {
		return // bootstrap not done; harmless during early startup
	}
	rows, err := dpi.GetPFDRules("")
	if err != nil {
		return
	}
	rules := make([]upfclient.PFDRule, 0, len(rows))
	for _, r := range rows {
		appID, _ := r["app_id"].(string)
		dt, _ := r["detection_type"].(string)
		pat, _ := r["pattern"].(string)
		rules = append(rules, upfclient.PFDRule{
			AppID: appID, DetectionType: dt, Pattern: pat,
		})
	}
	groups := upfclient.GroupRulesByApp(rules)
	_ = upfclient.DefaultRouter.PushPFDsToAll(groups)
}

func (s *Server) registerNSaaSRoutes() {
	r := s.Router

	// ── NSaaS (Network Slice as a Service) ───────────────────────────
	r.Get("/api/nsaas/stats", func(w http.ResponseWriter, rq *http.Request) {
		tenants, _ := nsaas.ListTenants()
		templates, _ := nsaas.ListTemplates()
		slices, _ := nsaas.ListSlices()
		active, provisioned := 0, 0
		for _, s := range slices {
			switch s.Status {
			case "active", "modifying":
				active++
			case "provisioned":
				provisioned++
			}
		}
		jsonReply(w, map[string]any{
			"tenants":            len(tenants),
			"templates":          len(templates),
			"active_slices":      active,
			"provisioned_slices": provisioned,
			"sla_violations":     0,
		})
	})

	r.Get("/api/nsaas/tenants", func(w http.ResponseWriter, rq *http.Request) {
		list, _ := nsaas.ListTenants()
		if list == nil {
			list = []nsaas.Tenant{}
		}
		jsonReply(w, map[string]any{"items": list})
	})
	r.Post("/api/nsaas/tenants", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			Name         string `json:"name"`
			ContactEmail string `json:"contact_email"`
			APIKey       string `json:"api_key"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		id, err := nsaas.CreateTenant(d.Name, d.ContactEmail, d.APIKey)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"id": id})
	})
	r.Delete("/api/nsaas/tenants/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err := nsaas.DeleteTenant(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]bool{"ok": true})
	})

	r.Get("/api/nsaas/templates", func(w http.ResponseWriter, rq *http.Request) {
		list, _ := nsaas.ListTemplates()
		if list == nil {
			list = []nsaas.Template{}
		}
		jsonReply(w, map[string]any{"items": list})
	})
	r.Post("/api/nsaas/templates", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			Name        string `json:"name"`
			SST         int    `json:"sst"`
			SD          string `json:"sd"`
			Description string `json:"description"`
			DefaultDNN  string `json:"default_dnn"`
			QoSProfile  string `json:"qos_profile"`
			SLADefaults string `json:"sla_defaults"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		id, err := nsaas.CreateTemplate(d.Name, d.SST, d.SD, d.Description, d.DefaultDNN, d.QoSProfile, d.SLADefaults)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"id": id})
	})
	r.Post("/api/nsaas/templates/seed", func(w http.ResponseWriter, rq *http.Request) {
		defaults := []struct {
			name, desc, dnn string
			sst             int
			sd              string
		}{
			{"eMBB", "Enhanced Mobile Broadband", "internet", 1, "000001"},
			{"URLLC", "Ultra-Reliable Low-Latency", "urllc", 2, "000002"},
			{"mMTC", "Massive Machine-Type Comm", "iot", 3, "000003"},
			{"V2X", "Vehicle-to-Everything", "v2x", 1, "000010"},
		}
		seeded := 0
		for _, d := range defaults {
			if _, err := nsaas.CreateTemplate(d.name, d.sst, d.sd, d.desc, d.dnn, "", ""); err == nil {
				seeded++
			}
		}
		jsonReply(w, map[string]int{"seeded": seeded})
	})
	r.Delete("/api/nsaas/templates/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err := nsaas.DeleteTemplate(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]bool{"ok": true})
	})

	r.Get("/api/nsaas/slices", func(w http.ResponseWriter, rq *http.Request) {
		list, _ := nsaas.ListSlices()
		if list == nil {
			list = []nsaas.Slice{}
		}
		jsonReply(w, map[string]any{"items": list})
	})
	r.Post("/api/nsaas/slices", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			TenantID   int64                  `json:"tenant_id"`
			TemplateID int64                  `json:"template_id"`
			Config     map[string]interface{} `json:"config"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		id, err := nsaas.ProvisionSlice(d.TemplateID, d.TenantID, d.Config)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Return the full slice so the caller can read back state
		// (status="provisioned", nssai_catalog_id, etc.) without an
		// extra GET. POST→create-then-fetch is the documented REST
		// shape; this route used to drop everything but the id.
		if sl, err := nsaas.GetSlice(id); err == nil && sl != nil {
			jsonReply(w, sl)
			return
		}
		jsonReply(w, map[string]any{"id": id})
	})
	r.Post("/api/nsaas/slices/{id}/activate", func(w http.ResponseWriter, rq *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err := nsaas.ActivateSlice(id); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Echo the activated slice (status="active") so callers can
		// confirm without a follow-up GET — same idiom as POST /slices.
		if sl, err := nsaas.GetSlice(id); err == nil && sl != nil {
			jsonReply(w, sl)
			return
		}
		jsonReply(w, map[string]bool{"ok": true})
	})
	r.Post("/api/nsaas/slices/{id}/decommission", func(w http.ResponseWriter, rq *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err := nsaas.DecommissionSlice(id); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Echo the slice (status="decommissioned") to match the same
		// idiom used by /provision and /activate.
		if sl, err := nsaas.GetSlice(id); err == nil && sl != nil {
			jsonReply(w, sl)
			return
		}
		jsonReply(w, map[string]bool{"ok": true})
	})
	r.Delete("/api/nsaas/slices/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err := nsaas.DeleteSlice(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]bool{"ok": true})
	})
	r.Get("/api/nsaas/slices/{id}/sla", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"status": "no_sla", "metrics": []any{}})
	})

	// ═══════════════════════════════════════════════════════════════════
	// AF sessions & events (af_api.py) — stub endpoints
	// ═══════════════════════════════════════════════════════════════════
	r.Get("/api/af/sessions", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"sessions": []any{}})
	})
	r.Post("/api/af/sessions", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"ok": false, "session_id": ""})
	})
	r.Delete("/api/af/sessions/{session_id}", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"ok": true})
	})
	r.Get("/api/af/events", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"subscriptions": []any{}})
	})
	r.Post("/api/af/events/subscribe", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"ok": false, "detail": "AF not ported"})
	})
	r.Delete("/api/af/events/{sub_id}", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"ok": true})
	})

	// Roaming routes moved to routes_roaming.go (TS 23.501 §5.6.3 +
	// TS 32.240 / 32.298 CDR ledger; wired to infra/roaming).

	// /api/npn/* lives in routes_npn.go (registered via
	// RegisterDomainRoutes; wired to security/npn package).

	// TSN routes live in routes_edge.go (full CRUD wired to edge/tsn).

	// ═══════════════════════════════════════════════════════════════════
	// NIDD (nidd.html template calls /api/nidd/*) — wired to
	// iot/nidd package. Spec anchors:
	//   - TS 23.502 §4.25       — 5GS NIDD procedures via NEF
	//   - TS 23.682 §5.13.2     — NIDD Configuration (CreateSession)
	//   - TS 23.682 §5.13.3     — MT NIDD delivery (send DL)
	//   - TS 23.682 §5.13.4     — MO NIDD delivery (receive UL)
	//   - TS 23.682 §5.13.5     — connection release (delete)
	//   - TS 29.122 §5          — T8/NEF northbound app-server registry
	// ═══════════════════════════════════════════════════════════════════
	r.Get("/api/nidd/stats", func(w http.ResponseWriter, rq *http.Request) {
		sessions, _ := nidd.ListSessions("")
		appServers, _ := nidd.ListAppServers()
		buffered := 0
		for _, s := range sessions {
			if s.Status == "buffered" {
				buffered++
			}
		}
		jsonReply(w, map[string]any{
			"ok":          true,
			"sessions":    len(sessions),
			"app_servers": len(appServers),
			"buffered":    buffered,
		})
	})
	r.Get("/api/nidd/sessions", func(w http.ResponseWriter, rq *http.Request) {
		imsi := rq.URL.Query().Get("imsi")
		sessions, err := nidd.ListSessions(imsi)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if sessions == nil {
			sessions = []nidd.Session{}
		}
		jsonReply(w, map[string]any{
			"ok": true, "sessions": sessions, "count": len(sessions),
		})
	})
	r.Post("/api/nidd/sessions", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI        string `json:"imsi"`
			APN         string `json:"apn"`
			AppServerID string `json:"app_server_id"`
			Config      struct {
				NotificationURL string `json:"notification_url"`
				MaxPayloadSize  int    `json:"max_payload_size"`
			} `json:"config"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.IMSI == "" {
			jsonError(w, "imsi required", http.StatusBadRequest)
			return
		}
		// APN defaults to "iot" — NIDD is a CIoT optimisation
		// (TS 23.682 §5.13) and the baseline catalogue carries an
		// `iot` APN row for exactly this path.
		if d.APN == "" {
			d.APN = "iot"
		}
		appURL := d.Config.NotificationURL
		if appURL == "" {
			appURL = "http://localhost/nidd/callback"
		}
		s, err := nidd.CreateSession(d.IMSI, "", d.APN, appURL)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{
			"ok":         true,
			"session_id": s.SessionID,
			"id":         s.SessionID,
			"status":     s.Status,
			"session":    s,
		})
	})
	r.Delete("/api/nidd/sessions/{sid}", func(w http.ResponseWriter, rq *http.Request) {
		sid := chi.URLParam(rq, "sid")
		if err := nidd.DeleteSessionBySessionID(sid); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "session_id": sid})
	})
	r.Get("/api/nidd/sessions/{sid}/log", func(w http.ResponseWriter, rq *http.Request) {
		sid := chi.URLParam(rq, "sid")
		s, err := nidd.GetSessionBySessionID(sid)
		if err != nil || s == nil {
			jsonReply(w, map[string]any{
				"ok": true, "entries": []any{}, "count": 0,
			})
			return
		}
		logs, _ := nidd.ListLogs(s.ID, 0)
		// Surface direction as lowercase ul/dl alongside the
		// uppercase storage form so dashboards and conformance
		// tooling that match against either spelling both work.
		entries := make([]map[string]any, 0, len(logs))
		for _, e := range logs {
			entries = append(entries, map[string]any{
				"id":         e.ID,
				"session_id": e.SessionID,
				"direction":  strings.ToLower(e.Direction),
				"type":       strings.ToLower(e.Direction),
				"data_hex":   e.DataHex,
				"length":     e.DataLength,
				"status":     e.Status,
				"created_at": e.CreatedAt,
			})
		}
		jsonReply(w, map[string]any{
			"ok": true, "entries": entries, "count": len(entries),
		})
	})
	r.Post("/api/nidd/sessions/{sid}/send", func(w http.ResponseWriter, rq *http.Request) {
		sid := chi.URLParam(rq, "sid")
		var d struct {
			DataHex string `json:"data_hex"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		s, err := nidd.GetSessionBySessionID(sid)
		if err != nil || s == nil {
			jsonError(w, "session not found", http.StatusNotFound)
			return
		}
		payload, err := hex.DecodeString(d.DataHex)
		if err != nil {
			jsonError(w, "data_hex must be hex-encoded", http.StatusBadRequest)
			return
		}
		entry, err := nidd.SendMT(s.ID, payload, "active")
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{
			"ok": true, "session_id": sid, "log": entry,
		})
	})
	r.Post("/api/nidd/sessions/{sid}/receive", func(w http.ResponseWriter, rq *http.Request) {
		sid := chi.URLParam(rq, "sid")
		var d struct {
			DataHex string `json:"data_hex"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		s, err := nidd.GetSessionBySessionID(sid)
		if err != nil || s == nil {
			jsonError(w, "session not found", http.StatusNotFound)
			return
		}
		payload, err := hex.DecodeString(d.DataHex)
		if err != nil {
			jsonError(w, "data_hex must be hex-encoded", http.StatusBadRequest)
			return
		}
		entry, err := nidd.SendMO(s.ID, payload)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{
			"ok": true, "session_id": sid, "log": entry,
		})
	})
	r.Post("/api/nidd/deliver-buffered", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"ok": true, "delivered": 0})
	})
	r.Get("/api/nidd/app-servers", func(w http.ResponseWriter, rq *http.Request) {
		servers, err := nidd.ListAppServers()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if servers == nil {
			servers = []nidd.AppServer{}
		}
		jsonReply(w, map[string]any{
			"ok": true, "app_servers": servers, "count": len(servers),
		})
	})
	r.Post("/api/nidd/app-servers", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			AppServerID string `json:"app_server_id"`
			Name        string `json:"name"`
			CallbackURL string `json:"callback_url"`
			Description string `json:"description"`
			AuthToken   string `json:"auth_token"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.AppServerID == "" {
			jsonError(w, "app_server_id required", http.StatusBadRequest)
			return
		}
		name := d.Name
		if name == "" {
			name = d.Description
		}
		a, err := nidd.RegisterAppServer(d.AppServerID, name, d.CallbackURL, d.AuthToken)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{
			"ok":            true,
			"app_server_id": a.AppServerID,
			"id":            a.AppServerID,
			"app_server":    a,
		})
	})
	r.Delete("/api/nidd/app-servers/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id := chi.URLParam(rq, "id")
		if err := nidd.DeleteAppServer(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "app_server_id": id})
	})
	// IoT NIDD sessions alias
	r.Get("/api/iot/nidd/sessions", func(w http.ResponseWriter, rq *http.Request) {
		imsi := rq.URL.Query().Get("imsi")
		sessions, _ := nidd.ListSessions(imsi)
		if sessions == nil {
			sessions = []nidd.Session{}
		}
		jsonReply(w, sessions)
	})

	// NTN routes moved to routes_ntn.go (TS 23.501 §5.4.10–§5.4.14
	// + TS 38.821).
	// N26 routes moved to routes_n26.go (TS 23.501 §5.17.2 / §5.17.3.4).

	// /api/li/* lives in routes_li.go (registered via RegisterDomainRoutes;
	// wired to security/li with the X-LI-Auth-Token gate at routes_li_auth.go).


	// /api/nwdaf/analytics + /api/nwdaf/subscriptions/* live in
	// routes_nwdaf_analytics.go (registered via RegisterDomainRoutes).

	// Disaster-Roaming routes moved to routes_disaster_roaming.go
	// (TS 23.501 §5.40 + TS 22.261 §6.31; wired to safety/disaster_roaming).

	// RACS routes moved to routes_racs.go (TS 22.261 §6.13 +
	// TS 24.501 §4.5; wired to safety/racs).

	// /api/trace/* lives in routes_trace.go (registered via RegisterDomainRoutes).

	// /api/ursp/* lives in routes_ursp.go (registered via RegisterDomainRoutes;
	// wired to nf/pcf/ursp — TS 23.503 §6.6, TS 24.501 §5.4.4).

	// /api/esim/order and /api/esim/profile/{iccid}/release live in
	// routes_esim.go (registered via RegisterDomainRoutes; wired to
	// services/esim — GSMA SGP.22 §3.0/§5.6 ES2+ + §3.5 audit).

	// ═══════════════════════════════════════════════════════════════════
	// DPI / Application Detection (dpi.html). TS 23.501 §5.8 — Traffic
	// Detection & Charging. App catalogue + PFD rules live in the local
	// DB and drive the SNI/DNS/IP/port classifiers in security/dpi.
	// PFCP-side push (TS 29.244 §6.2.5 PFD-Management) is not wired —
	// detections happen against the same operator-curated cache the
	// SMF would otherwise sync to the UPF.
	// ═══════════════════════════════════════════════════════════════════
	r.Post("/api/dpi/app", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			AppID           string `json:"app_id"`
			AppName         string `json:"app_name"`
			Category        string `json:"category"`
			QoSProfile      string `json:"qos_profile"`
			ChargingProfile string `json:"charging_profile"`
			Priority        int    `json:"priority"`
		}
		_ = json.NewDecoder(rq.Body).Decode(&d)
		if err := dpi.CreateApp(d.AppID, d.AppName, d.Category, d.QoSProfile, d.ChargingProfile, d.Priority); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Echo the row so the GUI can render the upserted record without a follow-up GET.
		if app, _ := dpi.GetApp(d.AppID); app != nil {
			jsonReply(w, app)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "app_id": d.AppID})
	})
	r.Post("/api/dpi/app/{id}/delete", func(w http.ResponseWriter, rq *http.Request) {
		appID := chi.URLParam(rq, "id")
		if err := dpi.DeleteApp(appID); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		// FK cascade dropped this app's PFD rules — resync the UPF.
		go pushDPIToUPF()
		jsonReply(w, map[string]any{"ok": true, "app_id": appID})
	})
	r.Post("/api/dpi/rule", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			AppID         string `json:"app_id"`
			DetectionType string `json:"detection_type"`
			Pattern       string `json:"pattern"`
		}
		_ = json.NewDecoder(rq.Body).Decode(&d)
		if err := dpi.AddPFDRule(d.AppID, d.DetectionType, d.Pattern); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		// TS 29.244 §6.2.5 — push the new PFD set to every UPF anchor
		// so the wire-side cache stays consistent with the operator's
		// catalogue. Best-effort, never fails the API call.
		go pushDPIToUPF()
		jsonReply(w, map[string]any{"ok": true, "app_id": d.AppID,
			"detection_type": d.DetectionType, "pattern": d.Pattern})
	})
	r.Post("/api/dpi/rule/{id}/delete", func(w http.ResponseWriter, rq *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		removed, err := dpi.DeletePFDRule(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		go pushDPIToUPF()
		jsonReply(w, map[string]any{"ok": removed, "id": id})
	})
	// /api/dpi/detect runs the four classifiers (TS 23.501 §5.8.2.4).
	// Query params drive which classifier fires; first non-empty match
	// wins. Returns {app, confidence, type} where confidence is 1.0 for
	// SNI/DNS exact matches and 0.5 for fallback IP/port matches.
	r.Get("/api/dpi/detect", func(w http.ResponseWriter, rq *http.Request) {
		q := rq.URL.Query()
		sni, domain, ip := q.Get("sni"), q.Get("domain"), q.Get("ip")
		port, _ := strconv.Atoi(q.Get("port"))
		var app, kind string
		var conf float64
		switch {
		case sni != "":
			app, kind, conf = dpi.DetectAppBySNI(sni), "sni", 1.0
		case domain != "":
			app, kind, conf = dpi.DetectAppByDNS(domain), "dns", 1.0
		case ip != "":
			app, kind, conf = dpi.DetectAppByIP(ip), "ip", 0.5
		case port > 0:
			app, kind, conf = dpi.DetectAppByPort(port), "port", 0.5
		}
		if app == "" {
			jsonReply(w, map[string]any{"app": nil, "confidence": 0, "type": kind})
			return
		}
		jsonReply(w, map[string]any{"app": app, "confidence": conf, "type": kind})
	})
	r.Post("/api/dpi/seed-defaults", func(w http.ResponseWriter, rq *http.Request) {
		dpi.SeedDefaultApps()
		apps, _ := dpi.ListApps()
		go pushDPIToUPF()
		jsonReply(w, map[string]any{"ok": true, "count": len(apps)})
	})
	// AI-assisted endpoints stay as stubs — they front a separate
	// LLM-router surface (oam/ai) that's wired through a different path.
	r.Post("/api/dpi/ai/generate-rules", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"rules": []any{}})
	})
	r.Post("/api/dpi/ai/classify", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"app": nil, "confidence": 0})
	})

	// ═══════════════════════════════════════════════════════════════════
	// AI Assistant (ai_assistant.html calls /api/ai/*)
	// ═══════════════════════════════════════════════════════════════════
	r.Get("/api/ai/status", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"enabled": false, "model": ""})
	})
	r.Get("/api/ai/config", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"provider": "", "model": ""})
	})
	r.Post("/api/ai/config", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"ok": true})
	})
	r.Post("/api/ai/chat", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"reply": "AI assistant not available in Go build"})
	})
	r.Post("/api/ai/troubleshoot", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"analysis": "AI troubleshooting not available in Go build"})
	})

	// /api/security/audit lives in routes_security.go (wired to
	// security/core_security; registered via RegisterDomainRoutes).

	// SMSF write endpoints moved to routes_smsf.go (validation +
	// envelope shape there).

	// ═══════════════════════════════════════════════════════════════════
	// MCX write endpoints (mcx.html)
	// ═══════════════════════════════════════════════════════════════════
	r.Post("/api/mcx/users", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"ok": true})
	})
	r.Post("/api/mcx/groups", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"ok": true})
	})
	r.Delete("/api/mcx/groups/{gid}", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"ok": true})
	})
	r.Post("/api/mcx/calls/{cid}/end", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"ok": true})
	})

	// CHF routes moved to routes_chf.go (TS 32.290 / TS 32.291;
	// wired to nf/chf with full charging-session lifecycle, quota
	// grants, balance management, and CDR export).

	// ═══════════════════════════════════════════════════════════════════
	// Connected UEs (slices.html)
	// ═══════════════════════════════════════════════════════════════════
	r.Get("/api/ues/connected", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"count": 0, "ues": []any{}})
	})

	// ═══════════════════════════════════════════════════════════════════
	// Resilience (resilience.html calls /api/resilience/*)
	// ═══════════════════════════════════════════════════════════════════
	r.Get("/api/resilience/stats", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"instances": 0, "sites": 0, "failovers": 0})
	})
	r.Get("/api/resilience/instances", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, []any{})
	})
	r.Post("/api/resilience/instances", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"ok": true})
	})
	r.Delete("/api/resilience/instances/{id}", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"ok": true})
	})
	r.Post("/api/resilience/instances/{id}/heartbeat", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"ok": true})
	})
	r.Get("/api/resilience/sites", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, []any{})
	})
	r.Post("/api/resilience/sites", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"ok": true})
	})
	r.Delete("/api/resilience/sites/{name}", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"ok": true})
	})
	r.Post("/api/resilience/sites/failover", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"ok": true})
	})
	r.Post("/api/resilience/failover", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"ok": true})
	})
	r.Get("/api/resilience/failover-log", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, []any{})
	})
	r.Get("/api/resilience/replication-status", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"status": "idle", "peers": []any{}})
	})

	// IOPS routes moved to routes_iops.go (TS 23.401 §K.1–§K.2.5;
	// wired to safety/iops).

	// MBS routes moved to routes_mbs.go (TS 23.247 §7; wired to
	// safety/mbs).

	// PWS routes moved to routes_pws.go (TS 23.501 §5.16.1 + TS 38.413
	// §8.9; wired to safety/pws).

	// ═══════════════════════════════════════════════════════════════════
	// NSACF (nsacf.html calls /api/nsacf/*)
	// ═══════════════════════════════════════════════════════════════════
	r.Get("/api/nsacf/stats", emptyObjRoute)
	r.Get("/api/nsacf/status", emptyObjRoute)
	r.Get("/api/nsacf/limits", emptyArrayRoute)
	r.Post("/api/nsacf/limits", func(w http.ResponseWriter, rq *http.Request) { jsonReply(w, map[string]any{"ok": true}) })
	r.Put("/api/nsacf/limits/{id}", func(w http.ResponseWriter, rq *http.Request) { jsonReply(w, map[string]any{"ok": true}) })
	r.Get("/api/nsacf/admissions", emptyArrayRoute)
	r.Post("/api/nsacf/admit", func(w http.ResponseWriter, rq *http.Request) { jsonReply(w, map[string]any{"ok": true}) })
	r.Post("/api/nsacf/release", func(w http.ResponseWriter, rq *http.Request) { jsonReply(w, map[string]any{"ok": true}) })
	r.Get("/api/nsacf/ue-slice-mbr", emptyArrayRoute)
	r.Post("/api/nsacf/ue-slice-mbr", func(w http.ResponseWriter, rq *http.Request) { jsonReply(w, map[string]any{"ok": true}) })
	r.Get("/api/nsacf/log", emptyArrayRoute)

	// /api/nwdaf/exposure/* lives in routes_nwdaf_exposure.go
	// (registered via RegisterDomainRoutes).

	// NTN Phase 2 routes moved to routes_ntn.go.

	// ═══════════════════════════════════════════════════════════════════
	// EAS sub-routes (eas.html calls /api/eas/*)
	// ═══════════════════════════════════════════════════════════════════
	r.Get("/api/eas/stats", emptyObjRoute)
	r.Get("/api/eas/servers", emptyArrayRoute)
	r.Post("/api/eas/servers", func(w http.ResponseWriter, rq *http.Request) { jsonReply(w, map[string]any{"ok": true}) })
	r.Delete("/api/eas/servers/{id}", func(w http.ResponseWriter, rq *http.Request) { jsonReply(w, map[string]any{"ok": true}) })

	// ═══════════════════════════════════════════════════════════════════
	// PIN (pin.html), ProSe (prose.html), SEAL (seal.html),
	// ISAC (isac.html), Ranging (ranging.html), RAN-Sharing,
	// USSD (ussd.html) — generic stub pattern
	// ═══════════════════════════════════════════════════════════════════
	for _, ns := range []string{"pin", "prose", "seal", "isac", "ranging", "ussd"} {
		ns := ns
		r.Get("/api/"+ns+"/stats", emptyObjRoute)
		r.Get("/api/"+ns+"/status", emptyObjRoute)
	}
	// PIN
	r.Get("/api/pin/elements", emptyArrayRoute)
	r.Post("/api/pin/elements", func(w http.ResponseWriter, rq *http.Request) { jsonReply(w, map[string]any{"ok": true}) })
	r.Delete("/api/pin/elements/{id}", func(w http.ResponseWriter, rq *http.Request) { jsonReply(w, map[string]any{"ok": true}) })
	// ProSe
	r.Get("/api/prose/discovery", emptyArrayRoute)
	r.Post("/api/prose/discovery", func(w http.ResponseWriter, rq *http.Request) { jsonReply(w, map[string]any{"ok": true}) })
	r.Get("/api/prose/relays", emptyArrayRoute)
	r.Post("/api/prose/relays", func(w http.ResponseWriter, rq *http.Request) { jsonReply(w, map[string]any{"ok": true}) })
	// SEAL
	r.Get("/api/seal/services", emptyArrayRoute)
	r.Post("/api/seal/services", func(w http.ResponseWriter, rq *http.Request) { jsonReply(w, map[string]any{"ok": true}) })
	// ISAC and Ranging routes live in routes_edge.go (full CRUD wired
	// to edge/isac and edge/ranging packages).
	// RAN Sharing routes moved to routes_ran_sharing.go (TS 22.261 §6.21
	// + TS 23.501 §5.17.4; wired to security/ran_sharing).
	// USSD routes moved to routes_ussd.go (TS 24.390 + TS 22.090;
	// wired to services/ussd).
}

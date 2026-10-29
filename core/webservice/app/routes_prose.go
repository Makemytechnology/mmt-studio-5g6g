// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_prose.go — REST surface for 5G Proximity Services (ProSe).
//
// Wires services/prose to /api/prose/* per the spec anchors that
// services/prose/{prose,discovery}.go cite in their headers:
//
//   - TS 22.278       5G ProSe service requirements
//   - TS 23.304 §5.1  Authorization & policy provisioning
//   - TS 23.304 §5.2  Direct Discovery (Models A and B; Open)
//   - TS 23.304 §5.3  Direct Communication on PC5
//                     (broadcast §5.3.2, groupcast §5.3.3,
//                      unicast §5.3.4)
//   - TS 23.304 §5.4  UE-to-Network relay
//   - TS 24.554 §5    NAS-layer ProSe procedures (deferred wire)
//   - TS 24.555 §5    PC5 signalling protocol (deferred wire)
//
// Each authorization gate respects the per-feature flag layout in
// prose_ue_config (TS 23.304 §5.1): discovery / communication /
// relay are independent. Restricted (closed) discovery (§5.2.4) is
// the deferred TODO.
package app

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/mmt/mmt-studio-core/services/prose"
)

func (s *Server) registerProSeRoutes() {
	r := s.Router

	// ── Status / stats ───────────────────────────────────────────
	r.Get("/api/prose/status", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, prose.GetStats())
	})

	// ── App registry (TS 23.304 §5.2 ProSe Application Code) ─────
	r.Get("/api/prose/apps", func(w http.ResponseWriter, _ *http.Request) {
		list, err := prose.ListApps()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []prose.App{}
		}
		jsonReply(w, list)
	})

	r.Post("/api/prose/apps", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			AppID         string `json:"app_id"`
			Name          string `json:"name"`
			ProseAppCode  string `json:"prose_app_code"`
			ValidityHours int    `json:"validity_hours"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.AppID == "" || d.ProseAppCode == "" {
			jsonError(w, "app_id and prose_app_code required", http.StatusBadRequest)
			return
		}
		id, err := prose.CreateApp(d.AppID, d.Name, d.ProseAppCode, d.ValidityHours)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReplyStatus(w, http.StatusCreated, map[string]any{
			"ok": true, "id": id, "app_id": d.AppID,
		})
	})

	r.Delete("/api/prose/apps/{app_id}", func(w http.ResponseWriter, rq *http.Request) {
		appID := chi.URLParam(rq, "app_id")
		if err := prose.DeleteApp(appID); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "app_id": appID})
	})

	// ── UE config / authorization (TS 23.304 §5.1) ───────────────
	r.Get("/api/prose/ue-config", func(w http.ResponseWriter, rq *http.Request) {
		imsi := rq.URL.Query().Get("imsi")
		if imsi == "" {
			jsonError(w, "imsi required", http.StatusBadRequest)
			return
		}
		cfg, err := prose.GetUEConfig(imsi)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if cfg == nil {
			jsonReply(w, map[string]any{"imsi": imsi, "authorized": false})
			return
		}
		jsonReply(w, cfg)
	})

	r.Post("/api/prose/ue-config", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI                 string `json:"imsi"`
			Authorized           int    `json:"authorized"`
			DiscoveryEnabled     int    `json:"discovery_enabled"`
			CommunicationEnabled int    `json:"communication_enabled"`
			RelayCapable         int    `json:"relay_capable"`
			RelayEnabled         int    `json:"relay_enabled"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.IMSI == "" {
			jsonError(w, "imsi required", http.StatusBadRequest)
			return
		}
		// TS 23.304 §5.1 — discovery / communication / relay are
		// independent flags, but `authorized` gates them all.
		if err := prose.SetUEConfig(d.IMSI, d.Authorized, d.DiscoveryEnabled,
			d.CommunicationEnabled, d.RelayCapable, d.RelayEnabled); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{
			"ok":                    true,
			"imsi":                  d.IMSI,
			"authorized":            d.Authorized,
			"discovery_enabled":     d.DiscoveryEnabled,
			"communication_enabled": d.CommunicationEnabled,
			"relay_capable":         d.RelayCapable,
			"relay_enabled":         d.RelayEnabled,
		})
	})

	r.Get("/api/prose/authorization/{imsi}", func(w http.ResponseWriter, rq *http.Request) {
		imsi := chi.URLParam(rq, "imsi")
		auth := prose.CheckAuthorization(imsi)
		if auth == nil {
			jsonError(w, "no prose ue-config for this IMSI", http.StatusNotFound)
			return
		}
		jsonReply(w, auth)
	})

	// ── Direct Discovery (TS 23.304 §5.2) ────────────────────────
	r.Post("/api/prose/discovery/announce", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI        string                 `json:"imsi"`
			AppCode     string                 `json:"app_code"`
			ValiditySec int                    `json:"validity_sec"`
			Metadata    map[string]interface{} `json:"metadata"`
			ServiceInfo string                 `json:"service_info"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.IMSI == "" || d.AppCode == "" {
			jsonError(w, "imsi and app_code required", http.StatusBadRequest)
			return
		}
		// Tester compatibility: service_info is folded into metadata.
		md := d.Metadata
		if md == nil && d.ServiceInfo != "" {
			md = map[string]interface{}{"service_info": d.ServiceInfo}
		}
		res := prose.Announce(d.IMSI, d.AppCode, d.ValiditySec, md)
		if ok, _ := res["ok"].(bool); !ok {
			// §5.1 authorization deny → 403.
			jsonReplyStatus(w, http.StatusForbidden, res)
			return
		}
		jsonReplyStatus(w, http.StatusCreated, res)
	})

	r.Delete("/api/prose/discovery/announce", func(w http.ResponseWriter, rq *http.Request) {
		imsi := rq.URL.Query().Get("imsi")
		appCode := rq.URL.Query().Get("app_code")
		if imsi == "" || appCode == "" {
			jsonError(w, "imsi and app_code required", http.StatusBadRequest)
			return
		}
		ok := prose.Withdraw(imsi, appCode)
		jsonReply(w, map[string]any{"ok": ok, "imsi": imsi, "app_code": appCode})
	})

	r.Post("/api/prose/discovery/monitor", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI        string                 `json:"imsi"`
			AppCode     string                 `json:"app_code"`
			ExcludeSelf *bool                  `json:"exclude_self"`
			Filters     map[string]interface{} `json:"filters"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.IMSI == "" {
			jsonError(w, "imsi required", http.StatusBadRequest)
			return
		}
		filters := d.Filters
		if filters == nil {
			filters = map[string]interface{}{}
		}
		if d.AppCode != "" {
			filters["app_code"] = d.AppCode
		}
		if d.ExcludeSelf != nil {
			filters["exclude_self"] = *d.ExcludeSelf
		}
		res := prose.Monitor(d.IMSI, filters)
		if ok, _ := res["ok"].(bool); !ok {
			jsonReplyStatus(w, http.StatusForbidden, res)
			return
		}
		jsonReply(w, res)
	})

	r.Get("/api/prose/discovery/active", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, map[string]any{"announcements": prose.GetActiveAnnouncements()})
	})

	// ── Direct Communication (TS 23.304 §5.3) ────────────────────
	r.Post("/api/prose/communication/setup", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			SourceIMSI  string `json:"source_imsi"`
			TargetIMSI  string `json:"target_imsi"`
			GroupID     string `json:"group_id"`
			SessionType string `json:"session_type"`
			Service     string `json:"service"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.SourceIMSI == "" {
			jsonError(w, "source_imsi required", http.StatusBadRequest)
			return
		}
		// TS 23.304 §5.3.3 vs §5.3.4 — explicit session_type wins,
		// otherwise infer from group_id vs target_imsi.
		stype := d.SessionType
		if stype == "" {
			if d.GroupID != "" {
				stype = "groupcast"
			} else {
				stype = "unicast"
			}
		}
		var res map[string]interface{}
		switch stype {
		case "unicast":
			if d.TargetIMSI == "" {
				jsonError(w, "target_imsi required for unicast", http.StatusBadRequest)
				return
			}
			res = prose.SetupUnicastWithAuth(d.SourceIMSI, d.TargetIMSI, d.Service)
		case "groupcast":
			if d.GroupID == "" {
				jsonError(w, "group_id required for groupcast", http.StatusBadRequest)
				return
			}
			res = prose.SetupGroupcastWithAuth(d.SourceIMSI, d.GroupID, d.Service)
		default:
			jsonError(w, "session_type must be unicast or groupcast", http.StatusBadRequest)
			return
		}
		if ok, _ := res["ok"].(bool); !ok {
			// §5.1 authorization deny → 403.
			jsonReplyStatus(w, http.StatusForbidden, res)
			return
		}
		// Tester compatibility: provide both `id` and `session_id`.
		if sid, ok := res["session_id"]; ok {
			res["id"] = sid
		}
		// 201 on success.
		jsonReplyStatus(w, http.StatusCreated, res)
	})

	r.Post("/api/prose/communication/{session_id}/release", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "session_id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid session_id", http.StatusBadRequest)
			return
		}
		jsonReply(w, prose.Release(id))
	})

	r.Get("/api/prose/sessions", func(w http.ResponseWriter, rq *http.Request) {
		imsi := rq.URL.Query().Get("imsi")
		status := rq.URL.Query().Get("status")
		list, err := prose.ListSessions(imsi, status)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []prose.Session{}
		}
		jsonReply(w, list)
	})

	// ── UE-to-Network relay (TS 23.304 §5.4) ─────────────────────
	r.Post("/api/prose/relay/register", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI             string `json:"imsi"`
			ServiceCode      string `json:"service_code"`
			RelayServiceCode string `json:"relay_service_code"`
			Connectivity     string `json:"connectivity"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.IMSI == "" {
			jsonError(w, "imsi required", http.StatusBadRequest)
			return
		}
		// TS 23.304 §5.4 calls it the relay service code; older
		// surfaces use service_code.
		code := d.ServiceCode
		if code == "" {
			code = d.RelayServiceCode
		}
		if code == "" {
			jsonError(w, "service_code (or relay_service_code) required",
				http.StatusBadRequest)
			return
		}
		res := prose.RegisterRelay(d.IMSI, code, d.Connectivity)
		if ok, _ := res["ok"].(bool); !ok {
			jsonReplyStatus(w, http.StatusForbidden, res)
			return
		}
		jsonReplyStatus(w, http.StatusCreated, res)
	})

	r.Post("/api/prose/relay/discover", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI             string `json:"imsi"`
			ServiceCode      string `json:"service_code"`
			RelayServiceCode string `json:"relay_service_code"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		// Allow callers to omit imsi (treat as a discovery probe);
		// authorizeUE will reject if the imsi isn't authorised.
		code := d.ServiceCode
		if code == "" {
			code = d.RelayServiceCode
		}
		res := prose.DiscoverRelays(d.IMSI, code)
		if ok, _ := res["ok"].(bool); !ok {
			jsonReplyStatus(w, http.StatusForbidden, res)
			return
		}
		jsonReply(w, res)
	})

	r.Post("/api/prose/relay/connect", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			RemoteIMSI string `json:"remote_imsi"`
			RelayIMSI  string `json:"relay_imsi"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.RemoteIMSI == "" || d.RelayIMSI == "" {
			jsonError(w, "remote_imsi and relay_imsi required", http.StatusBadRequest)
			return
		}
		res := prose.ConnectViaRelay(d.RemoteIMSI, d.RelayIMSI)
		if ok, _ := res["ok"].(bool); !ok {
			// §5.1 deny vs. relay-not-available — both surface as 4xx.
			// "not authorized" → 403, "relay UE not available" → 404.
			emsg, _ := res["error"].(string)
			code := http.StatusBadRequest
			switch emsg {
			case "remote not authorized":
				code = http.StatusForbidden
			case "relay UE not available", "relay expired":
				code = http.StatusNotFound
			}
			jsonReplyStatus(w, code, res)
			return
		}
		jsonReplyStatus(w, http.StatusCreated, res)
	})

	r.Get("/api/prose/relays/active", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, map[string]any{"relays": prose.GetActiveRelays()})
	})
}

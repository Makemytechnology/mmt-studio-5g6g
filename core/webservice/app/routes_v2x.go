// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_v2x.go — REST surface for the V2X service tier.
//
// Wires services/v2x to /api/v2x/* per the spec anchors that
// services/v2x/v2x.go cites in its package header:
//
//   - TS 23.287 §5.4.4   PQI / PC5 QoS table CRUD
//   - TS 23.287 §5.5     V2X subscription (UE authorization)
//   - TS 23.287 §5.1.2   V2X policy / parameter provisioning
//   - TS 23.287 §4.4     V2X policy delivery (PCF→UE), audit log
//   - TS 24.587 §5       UE Policy Container envelope (deferred)
//
// Read paths are operator-curated catalogs; write paths mutate the
// SQLite tables in db/schemas/v2x.go. The wire-format envelope (UE
// Policy Container per TS 24.501 §D.6.1) is not modelled here — the
// route returns the policy body that a future AMF push would wrap.
package app

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/mmt/mmt-studio-core/services/v2x"
)

func (s *Server) registerV2XRoutes() {
	r := s.Router

	// ── Status / catalog read paths ──────────────────────────────

	r.Get("/api/v2x/status", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, v2x.Status())
	})

	// PQI catalog (TS 23.287 §5.4.4 Table 5.4.4-1)
	r.Get("/api/v2x/service-types", func(w http.ResponseWriter, _ *http.Request) {
		list, err := v2x.ListServiceTypes()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []v2x.ServiceType{}
		}
		jsonReply(w, list)
	})

	r.Get("/api/v2x/service-types/{pqi}", func(w http.ResponseWriter, rq *http.Request) {
		pqi, err := strconv.Atoi(chi.URLParam(rq, "pqi"))
		if err != nil {
			jsonError(w, "invalid pqi", http.StatusBadRequest)
			return
		}
		st, err := v2x.GetServiceType(pqi)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if st == nil {
			jsonError(w, "not found", http.StatusNotFound)
			return
		}
		jsonReply(w, st)
	})

	r.Post("/api/v2x/service-types", func(w http.ResponseWriter, rq *http.Request) {
		var st v2x.ServiceType
		if !decodeJSON(w, rq, &st) {
			return
		}
		if st.PQI <= 0 || st.ServiceName == "" || st.ResourceType == "" {
			jsonError(w, "service_name, pqi, resource_type required", http.StatusBadRequest)
			return
		}
		// TS 23.287 §5.4.4 enumerates resource_type ∈
		// {GBR, NonGBR, DelCritGBR}; the DDL CHECK enforces it but
		// a friendlier 400 here saves a SQL round-trip.
		switch st.ResourceType {
		case "GBR", "NonGBR", "DelCritGBR":
		default:
			jsonError(w, "resource_type must be GBR|NonGBR|DelCritGBR", http.StatusBadRequest)
			return
		}
		id, err := v2x.CreateServiceType(st)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReplyStatus(w, http.StatusCreated, map[string]any{"ok": true, "id": id, "pqi": st.PQI})
	})

	r.Put("/api/v2x/service-types/{pqi}", func(w http.ResponseWriter, rq *http.Request) {
		pqi, err := strconv.Atoi(chi.URLParam(rq, "pqi"))
		if err != nil {
			jsonError(w, "invalid pqi", http.StatusBadRequest)
			return
		}
		var st v2x.ServiceType
		if !decodeJSON(w, rq, &st) {
			return
		}
		st.PQI = pqi
		if err := v2x.UpdateServiceType(pqi, st); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "pqi": pqi})
	})

	r.Delete("/api/v2x/service-types/{pqi}", func(w http.ResponseWriter, rq *http.Request) {
		pqi, err := strconv.Atoi(chi.URLParam(rq, "pqi"))
		if err != nil {
			jsonError(w, "invalid pqi", http.StatusBadRequest)
			return
		}
		if err := v2x.DeleteServiceType(pqi); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})

	// ── Operator config (NR PC5 frequencies, enables, etc.) ──────

	r.Get("/api/v2x/config", func(w http.ResponseWriter, _ *http.Request) {
		list, err := v2x.ListConfig()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []v2x.Config{}
		}
		jsonReply(w, list)
	})

	r.Get("/api/v2x/config/{key}", func(w http.ResponseWriter, rq *http.Request) {
		key := chi.URLParam(rq, "key")
		val, err := v2x.GetConfig(key)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"key": key, "value": val})
	})

	r.Post("/api/v2x/config", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.Key == "" {
			jsonError(w, "key required", http.StatusBadRequest)
			return
		}
		if err := v2x.SetConfig(d.Key, d.Value); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "key": d.Key, "value": d.Value})
	})

	r.Get("/api/v2x/frequencies", func(w http.ResponseWriter, _ *http.Request) {
		freqs := v2x.LoadFrequencies()
		if freqs == nil {
			freqs = []int{}
		}
		jsonReply(w, map[string]any{"frequencies": freqs})
	})

	// ── Subscription / authorization (TS 23.287 §5.2 + §5.5) ─────

	r.Get("/api/v2x/subscription/{imsi}", func(w http.ResponseWriter, rq *http.Request) {
		imsi := chi.URLParam(rq, "imsi")
		sub := v2x.LoadSubscription(imsi)
		if sub == nil {
			jsonReply(w, map[string]any{"imsi": imsi, "v2x_authorized": false})
			return
		}
		jsonReply(w, map[string]any{
			"imsi":               imsi,
			"v2x_authorized":     sub.V2XAuthorized,
			"v2x_ue_type":        sub.V2XUEType,
			"v2x_pc5_ambr_kbps":  sub.V2XPC5AMBRKbps,
		})
	})

	r.Post("/api/v2x/authorize", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI         string `json:"imsi"`
			UEType       string `json:"ue_type"`
			PC5AMBRKbps  int    `json:"pc5_ambr_kbps"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.IMSI == "" {
			jsonError(w, "imsi required", http.StatusBadRequest)
			return
		}
		if err := v2x.AuthorizeUE(d.IMSI, d.UEType, d.PC5AMBRKbps); err != nil {
			// TS 23.287 §5.5 enum violation → 400. The route is
			// upserting (creates the UE row if missing) so a
			// not-found doesn't surface here.
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{
			"ok":                 true,
			"imsi":               d.IMSI,
			"v2x_authorized":     true,
			"v2x_ue_type":        d.UEType,
			"v2x_pc5_ambr_kbps":  d.PC5AMBRKbps,
		})
	})

	r.Post("/api/v2x/deauthorize", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI string `json:"imsi"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.IMSI == "" {
			jsonError(w, "imsi required", http.StatusBadRequest)
			return
		}
		if err := v2x.DeauthorizeUE(d.IMSI); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "imsi": d.IMSI, "v2x_authorized": false})
	})

	r.Get("/api/v2x/pc5-qos/{imsi}", func(w http.ResponseWriter, rq *http.Request) {
		imsi := chi.URLParam(rq, "imsi")
		// TS 23.287 §5.4 — only authorised UEs receive PC5 QoS table.
		if !v2x.IsAuthorized(imsi) {
			jsonError(w, "ue not authorized for v2x", http.StatusForbidden)
			return
		}
		list := v2x.GetPC5QoSParams(imsi)
		if list == nil {
			list = []v2x.ServiceType{}
		}
		jsonReply(w, map[string]any{"imsi": imsi, "pc5_qos_params": list})
	})

	// ── Authorized PLMN list (TS 23.287 §5.1.2) ──────────────────

	r.Get("/api/v2x/authorized-plmns/{imsi}", func(w http.ResponseWriter, rq *http.Request) {
		imsi := chi.URLParam(rq, "imsi")
		plmns := v2x.ListAuthorizedPLMNs(imsi)
		if plmns == nil {
			plmns = []string{}
		}
		jsonReply(w, map[string]any{"imsi": imsi, "authorized_plmns": plmns})
	})

	r.Post("/api/v2x/authorized-plmns", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI    string `json:"imsi"`
			PLMNID  string `json:"plmn_id"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.IMSI == "" || d.PLMNID == "" {
			jsonError(w, "imsi and plmn_id required", http.StatusBadRequest)
			return
		}
		if err := v2x.AddAuthorizedPLMN(d.IMSI, d.PLMNID); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReplyStatus(w, http.StatusCreated, map[string]any{
			"ok":       true,
			"imsi":     d.IMSI,
			"plmn_id":  d.PLMNID,
		})
	})

	r.Delete("/api/v2x/authorized-plmns", func(w http.ResponseWriter, rq *http.Request) {
		imsi := rq.URL.Query().Get("imsi")
		plmn := rq.URL.Query().Get("plmn_id")
		if imsi == "" || plmn == "" {
			jsonError(w, "imsi and plmn_id required", http.StatusBadRequest)
			return
		}
		if err := v2x.DeleteAuthorizedPLMN(imsi, plmn); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})

	// ── Policy provisioning (TS 23.287 §5.1.2 + TS 24.587 §5) ────

	r.Post("/api/v2x/policy/provision", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI string `json:"imsi"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.IMSI == "" {
			jsonError(w, "imsi required", http.StatusBadRequest)
			return
		}
		body := v2x.ProvisionPolicy(d.IMSI)
		if body == nil {
			// Spec: only authorised UEs receive policy.
			jsonError(w, "ue not authorized for v2x", http.StatusForbidden)
			return
		}
		jsonReply(w, map[string]any{
			"ok":     true,
			"imsi":   d.IMSI,
			"policy": body,
		})
	})

	r.Get("/api/v2x/policy/log", func(w http.ResponseWriter, rq *http.Request) {
		imsi := rq.URL.Query().Get("imsi")
		limit := 100
		if l, err := strconv.Atoi(rq.URL.Query().Get("limit")); err == nil && l > 0 {
			limit = l
		}
		entries, err := v2x.ListPolicyLog(imsi, limit)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if entries == nil {
			entries = []v2x.PolicyLogEntry{}
		}
		jsonReply(w, map[string]any{"entries": entries, "count": len(entries)})
	})
}

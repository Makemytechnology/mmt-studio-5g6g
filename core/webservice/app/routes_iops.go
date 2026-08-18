// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_iops.go — REST surface for Isolated E-UTRAN Operation for
// Public Safety (IOPS).
//
// Wires `safety/iops` to /api/iops/*. The package owns the per-gNB
// IOPS lifecycle (normal → backhaul_lost → iops_activated →
// restoring → restored | failed), pre-cached AKA tuples for Local
// EPC authentication, the curated local-service catalogue, and the
// active local-session ledger.
//
// Spec anchors (verified against local TS PDFs by speccheck):
//
//   - TS 23.401 §K.1   — IOPS general description.
//   - TS 23.401 §K.2.1 — Operation of isolated public safety networks.
//   - TS 23.401 §K.2.2 — UE configuration (IOPS-enabled USIM, dedicated PLMN).
//   - TS 23.401 §K.2.3 — IOPS network configuration (cached AKA tuples).
//   - TS 23.401 §K.2.4 — IOPS network establishment / termination.
//   - TS 23.401 §K.2.5 — UE mobility within / out of IOPS.
//   - TS 22.346        — IOPS service requirements (TODO; not loaded).
//
// All response shapes match `templates/iops.html`: every endpoint
// returns `{ok, ...}` keyed by domain noun.
package app

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/mmt/mmt-studio-core/safety/iops"
)

func (s *Server) registerIOPSRoutes() {
	r := s.Router

	// ── Stats / dashboard ─────────────────────────────────────────
	r.Get("/api/iops/stats", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, map[string]any{"ok": true, "stats": iops.GetStats()})
	})

	// /status returns the per-gNB current state for the table.
	r.Get("/api/iops/status", func(w http.ResponseWriter, _ *http.Request) {
		cfgs, err := iops.ListConfigs()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		states := iops.GetAllStates()
		out := make([]map[string]any, 0, len(cfgs))
		for _, c := range cfgs {
			gnbID, _ := c["gnb_id"].(string)
			st, ok := states[gnbID]
			if !ok {
				st = string(iops.StateNormal)
			}
			out = append(out, map[string]any{
				"gnb_id":             gnbID,
				"state":              st,
				"iops_enabled":       toIntCol(c["iops_enabled"]) == 1,
				"local_auth_enabled": toIntCol(c["local_auth_enabled"]) == 1,
				"max_local_ues":      c["max_local_ues"],
				"local_ip_pool":      c["local_ip_pool"],
			})
		}
		jsonReply(w, map[string]any{"ok": true, "gnbs": out})
	})

	// ── State machine — Declare / Restore (TS 23.401 §K.2.4) ─────
	r.Post("/api/iops/declare", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			GnbID  string `json:"gnb_id"`
			Reason string `json:"reason"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.GnbID == "" {
			jsonError(w, "gnb_id required", http.StatusBadRequest)
			return
		}
		if d.Reason == "" {
			d.Reason = "backhaul_failure"
		}
		// Two-step: backhaul_lost → iops_activated. Both events are
		// recorded so the audit log shows both transitions.
		ev1 := iops.DetectBackhaulLoss(d.GnbID, d.Reason)
		ev2 := iops.ActivateIOPS(d.GnbID)
		jsonReply(w, map[string]any{
			"ok":            true,
			"gnb_id":        d.GnbID,
			"state":         iops.GetState(d.GnbID),
			"backhaul_lost": ev1,
			"iops_active":   ev2,
		})
	})

	r.Post("/api/iops/restore", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			GnbID string `json:"gnb_id"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.GnbID == "" {
			jsonError(w, "gnb_id required", http.StatusBadRequest)
			return
		}
		// Two-step: restoring → normal. The event log stamps both.
		ev1 := iops.BeginRestoration(d.GnbID)
		ev2 := iops.CompleteRestoration(d.GnbID)
		jsonReply(w, map[string]any{
			"ok":         true,
			"gnb_id":     d.GnbID,
			"state":      iops.GetState(d.GnbID),
			"restoring":  ev1,
			"restored":   ev2,
		})
	})

	// ── Per-gNB config (TS 23.401 §K.2.3) ─────────────────────────
	r.Get("/api/iops/config", func(w http.ResponseWriter, _ *http.Request) {
		list, err := iops.ListConfigs()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []map[string]interface{}{}
		}
		jsonReply(w, map[string]any{"ok": true, "configs": list})
	})

	r.Get("/api/iops/config/{gnb}", func(w http.ResponseWriter, rq *http.Request) {
		cfg, err := iops.GetConfig(chi.URLParam(rq, "gnb"))
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if cfg == nil {
			jsonError(w, "no config for gnb", http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "config": cfg})
	})

	r.Post("/api/iops/config", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			GnbID            string `json:"gnb_id"`
			IOPSEnabled      int    `json:"iops_enabled"`
			LocalAuthEnabled int    `json:"local_auth_enabled"`
			MaxLocalUEs      int    `json:"max_local_ues"`
			LocalIPPool      string `json:"local_ip_pool"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.GnbID == "" {
			jsonError(w, "gnb_id required", http.StatusBadRequest)
			return
		}
		if err := iops.UpsertConfig(d.GnbID,
			d.IOPSEnabled == 1, d.LocalAuthEnabled == 1,
			d.MaxLocalUEs, d.LocalIPPool); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		cfg, _ := iops.GetConfig(d.GnbID)
		jsonReplyStatus(w, http.StatusCreated,
			map[string]any{"ok": true, "config": cfg})
	})

	// ── Cached AKA tuples (TS 23.401 §K.2.3) ──────────────────────
	r.Get("/api/iops/cache/{gnb}", func(w http.ResponseWriter, rq *http.Request) {
		gnb := chi.URLParam(rq, "gnb")
		creds, err := iops.ListCachedCredentials(gnb)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if creds == nil {
			creds = []map[string]interface{}{}
		}
		jsonReply(w, map[string]any{
			"ok":          true,
			"gnb_id":      gnb,
			"count":       len(creds),
			"credentials": creds,
		})
	})

	r.Post("/api/iops/cache-credentials", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			GnbID       string                   `json:"gnb_id"`
			Credentials []map[string]interface{} `json:"credentials"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.GnbID == "" {
			jsonError(w, "gnb_id required", http.StatusBadRequest)
			return
		}
		cached := 0
		var lastErr error
		for _, c := range d.Credentials {
			cred := iops.CachedCredential{
				GnbID:       d.GnbID,
				IMSI:        toStringCol(c["imsi"]),
				RandHex:     toStringCol(c["rand_hex"]),
				AutnHex:     toStringCol(c["autn_hex"]),
				XresStarHex: toStringCol(c["xres_star_hex"]),
				KseafHex:    toStringCol(c["kseaf_hex"]),
				ExpiresAt:   toStringCol(c["expires_at"]),
			}
			if err := iops.CacheCredential(cred); err != nil {
				lastErr = err
				continue
			}
			cached++
		}
		if cached == 0 && lastErr != nil {
			jsonError(w, lastErr.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{
			"ok":     true,
			"gnb_id": d.GnbID,
			"cached": cached,
		})
	})

	r.Delete("/api/iops/cache/{gnb}/{imsi}", func(w http.ResponseWriter, rq *http.Request) {
		if err := iops.DeleteCachedCredential(chi.URLParam(rq, "gnb"),
			chi.URLParam(rq, "imsi")); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})

	// Local AKA probe — does (gnb_id, imsi) have a fresh cached tuple?
	r.Get("/api/iops/local-auth", func(w http.ResponseWriter, rq *http.Request) {
		gnb := rq.URL.Query().Get("gnb_id")
		imsi := rq.URL.Query().Get("imsi")
		if gnb == "" || imsi == "" {
			jsonError(w, "gnb_id and imsi required", http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{
			"ok":     true,
			"result": iops.LocalAuthenticate(gnb, imsi),
		})
	})

	// ── Local sessions (TS 23.401 §K.2.4) ─────────────────────────
	r.Get("/api/iops/local-sessions", func(w http.ResponseWriter, rq *http.Request) {
		gnb := rq.URL.Query().Get("gnb_id")
		list, err := iops.ListLocalSessions(gnb)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []map[string]interface{}{}
		}
		jsonReply(w, map[string]any{"ok": true, "sessions": list})
	})

	r.Post("/api/iops/local-sessions", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			GnbID       string `json:"gnb_id"`
			IMSI        string `json:"imsi"`
			ServiceType string `json:"service_type"`
			IPAddress   string `json:"ip_address"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.GnbID == "" || d.IMSI == "" {
			jsonError(w, "gnb_id and imsi required", http.StatusBadRequest)
			return
		}
		// service_type is CHECK-constrained at the schema layer; we
		// surface a clean 400 instead of a 500-from-SQLite.
		switch d.ServiceType {
		case "voice", "data", "ptt", "emergency":
		default:
			jsonError(w, "service_type must be voice|data|ptt|emergency",
				http.StatusBadRequest)
			return
		}
		id, err := iops.CreateLocalSession(d.GnbID, d.IMSI, d.ServiceType,
			d.IPAddress)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReplyStatus(w, http.StatusCreated,
			map[string]any{"ok": true, "id": id})
	})

	r.Post("/api/iops/local-sessions/{id}/release", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		if err := iops.ReleaseLocalSession(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})

	// ── Service availability probe + event log ───────────────────
	r.Get("/api/iops/service-available", func(w http.ResponseWriter, rq *http.Request) {
		gnb := rq.URL.Query().Get("gnb_id")
		svc := rq.URL.Query().Get("service")
		jsonReply(w, map[string]any{
			"ok":        true,
			"gnb_id":    gnb,
			"service":   svc,
			"available": iops.CheckServiceAvailable(gnb, svc),
		})
	})

	r.Get("/api/iops/services", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, map[string]any{
			"ok":       true,
			"services": iops.DefaultLocalServices(),
		})
	})

	r.Get("/api/iops/events", func(w http.ResponseWriter, rq *http.Request) {
		gnb := rq.URL.Query().Get("gnb_id")
		limit := 50
		if v := rq.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		list, err := iops.GetEvents(gnb, limit)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []map[string]interface{}{}
		}
		jsonReply(w, map[string]any{"ok": true, "events": list})
	})
}

// toIntCol coerces a SQL-scanned column (may arrive as int64) to int.
func toIntCol(v interface{}) int {
	switch vv := v.(type) {
	case int64:
		return int(vv)
	case int:
		return vv
	case float64:
		return int(vv)
	}
	return 0
}

// toStringCol coerces a SQL-scanned column to string.
func toStringCol(v interface{}) string {
	switch vv := v.(type) {
	case string:
		return vv
	case []byte:
		return string(vv)
	}
	if v == nil {
		return ""
	}
	return ""
}

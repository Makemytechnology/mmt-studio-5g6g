// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_racs.go — REST surface for Restricted Access Control.
//
// Wires `safety/racs` to /api/racs/*. The package owns the four
// restriction levels (normal, restricted, emergency_only,
// full_lockdown), per-access-category barring factors, the per-IMSI
// admission gate, and the audit log.
//
// Spec anchors (verified against local TS PDFs by speccheck):
//
//   - TS 22.011 §4    Service accessibility umbrella.
//   - TS 22.261 §6.13 Access control requirements (priority + barring).
//   - TS 23.501 §5.18 Service Continuity, including AC restrictions.
//   - TS 24.501 §4.5  Unified Access Control (UAC) — operator
//                     barring categories.
//
// All response shapes match `templates/racs.html`: flat objects (or
// arrays) keyed by domain noun — no `{ok, ...}` wrapping (the panel
// uses `d.restriction_level`, `rows.forEach`, etc. directly).
package app

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/mmt/mmt-studio-core/safety/racs"
)

func (s *Server) registerRACSRoutes() {
	r := s.Router

	// ── Status / dashboard ───────────────────────────────────────
	r.Get("/api/racs/status", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, racs.GetRestrictionStatus())
	})
	r.Get("/api/racs/stats", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, racs.GetAccessStats())
	})

	// ── Restriction-level activation (TS 23.501 §5.18) ───────────
	r.Post("/api/racs/activate", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			Level       string `json:"level"`
			Reason      string `json:"reason"`
			Areas       string `json:"areas"`
			ActivatedBy string `json:"activated_by"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		// CHECK constraint at the schema level rejects bad values too,
		// but we surface a clean 400 instead of a 500-from-SQLite.
		switch d.Level {
		case "normal", "restricted", "emergency_only", "full_lockdown":
		default:
			jsonError(w,
				"level must be normal|restricted|emergency_only|full_lockdown",
				http.StatusBadRequest)
			return
		}
		if d.ActivatedBy == "" {
			d.ActivatedBy = "operator"
		}
		if err := racs.ActivateRestriction(d.Level, d.Reason, d.Areas,
			d.ActivatedBy); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, racs.GetRestrictionStatus())
	})

	r.Post("/api/racs/deactivate", func(w http.ResponseWriter, _ *http.Request) {
		racs.DeactivateRestriction()
		jsonReply(w, racs.GetRestrictionStatus())
	})

	// ── Per-category barring factor (TS 24.501 §4.5) ─────────────
	r.Get("/api/racs/barring", func(w http.ResponseWriter, _ *http.Request) {
		list, err := racs.GetBarringConfigs()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []map[string]interface{}{}
		}
		jsonReply(w, list)
	})

	r.Post("/api/racs/barring", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			AccessCategory int     `json:"access_category"`
			BarringFactor  float64 `json:"barring_factor"`
			BarringTimeS   int     `json:"barring_time_s"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		// TS 24.501 §4.5.2 — UAC access-category vocabulary is
		// integer-coded; we accept 0–63 as the spec's reserved range.
		if d.AccessCategory < 0 || d.AccessCategory > 63 {
			jsonError(w, "access_category must be in [0, 63]",
				http.StatusBadRequest)
			return
		}
		if d.BarringFactor < 0 || d.BarringFactor > 1 {
			jsonError(w, "barring_factor must be in [0.0, 1.0]",
				http.StatusBadRequest)
			return
		}
		racs.SetBarringFactor(d.AccessCategory, d.BarringFactor, d.BarringTimeS)
		jsonReply(w, map[string]any{"ok": true,
			"access_category": d.AccessCategory})
	})

	r.Delete("/api/racs/barring/{cat}", func(w http.ResponseWriter, rq *http.Request) {
		// "Reset" — set factor=1.0 (no barring), time=0, disabled.
		cat, err := strconv.Atoi(chi.URLParam(rq, "cat"))
		if err != nil {
			jsonError(w, "invalid access_category", http.StatusBadRequest)
			return
		}
		racs.SetBarringFactor(cat, 1.0, 0)
		jsonReply(w, map[string]any{"ok": true, "access_category": cat})
	})

	// ── Admission gate ───────────────────────────────────────────
	r.Post("/api/racs/check-access", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI           string `json:"imsi"`
			AccessCategory int    `json:"access_category"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.IMSI == "" {
			jsonError(w, "imsi required", http.StatusBadRequest)
			return
		}
		jsonReply(w, racs.CheckAccess(d.IMSI, d.AccessCategory))
	})

	// ── Audit log ────────────────────────────────────────────────
	r.Get("/api/racs/access-log", func(w http.ResponseWriter, rq *http.Request) {
		limit := 100
		if v := rq.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		list, err := racs.GetAccessLog(limit)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []map[string]interface{}{}
		}
		jsonReply(w, list)
	})
}

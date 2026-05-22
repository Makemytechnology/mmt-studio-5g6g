// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_disaster_roaming.go — REST surface for the Disaster
// Roaming control plane.
//
// Wires `safety/disaster_roaming` to /api/disaster-roaming/*. Disaster
// roaming lets a serving PLMN admit UEs from a partner PLMN that has
// suffered a disaster outage, even when no normal roaming agreement
// exists. The package owns the declaration ledger, the per-IMSI
// admission gate, the active-roaming-UE register, and the audit log.
//
// Spec anchors (verified against local TS PDFs by speccheck):
//
//   - TS 23.501 §5.40    Disaster Roaming for PLMNs (5GC architecture).
//   - TS 23.501 §5.40.2  Disaster condition handling — declaration
//                        and lifecycle.
//   - TS 23.501 §5.40.3  Restrictions of services and applications.
//   - TS 22.261 §6.31    Service requirements: Disaster Roaming.
//
// Response shapes match `templates/disaster_roaming.html`: flat
// objects (not `{ok, ...}` wrapped) keyed by domain noun.
package app

import (
	"net/http"
	"strconv"

	"github.com/mmt/mmt-studio-core/safety/disaster_roaming"
)

func (s *Server) registerDisasterRoamingRoutes() {
	r := s.Router

	// ── Status / dashboard ───────────────────────────────────────
	r.Get("/api/disaster-roaming/status", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, disaster_roaming.GetDisasterStatus())
	})

	r.Get("/api/disaster-roaming/stats", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, disaster_roaming.GetDRStats())
	})

	// ── Declaration lifecycle (TS 23.501 §5.40.2) ────────────────
	r.Post("/api/disaster-roaming/declare", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			Name           string `json:"name"`
			Reason         string `json:"reason"`
			AffectedAreas  string `json:"affected_areas"`
			DeclaredBy     string `json:"declared_by"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.Name == "" {
			jsonError(w, "name required", http.StatusBadRequest)
			return
		}
		if d.DeclaredBy == "" {
			d.DeclaredBy = "operator"
		}
		id, err := disaster_roaming.DeclareDisaster(d.Name, d.Reason,
			d.AffectedAreas, d.DeclaredBy)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReplyStatus(w, http.StatusCreated, map[string]any{
			"ok":             true,
			"declaration_id": id,
			"name":           d.Name,
		})
	})

	r.Post("/api/disaster-roaming/end", func(w http.ResponseWriter, rq *http.Request) {
		// Body is optional; bare POST ends every active declaration.
		var d struct {
			DeclarationID int64 `json:"declaration_id,omitempty"`
		}
		if rq.ContentLength > 0 {
			_ = decodeJSON(w, rq, &d)
		}
		if d.DeclarationID > 0 {
			if err := disaster_roaming.EndDisasterByID(d.DeclarationID); err != nil {
				jsonError(w, err.Error(), http.StatusBadRequest)
				return
			}
		} else {
			disaster_roaming.EndDisaster()
		}
		jsonReply(w, map[string]any{"ok": true})
	})

	r.Get("/api/disaster-roaming/declarations", func(w http.ResponseWriter, _ *http.Request) {
		list, err := disaster_roaming.GetAllDeclarations()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []map[string]interface{}{}
		}
		jsonReply(w, list)
	})

	// ── Per-UE admission gate (TS 23.501 §5.40.3) ────────────────
	r.Post("/api/disaster-roaming/check", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI  string `json:"imsi"`
			HPLMN string `json:"hplmn"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.IMSI == "" || d.HPLMN == "" {
			jsonError(w, "imsi and hplmn required", http.StatusBadRequest)
			return
		}
		jsonReply(w, disaster_roaming.CheckDisasterRoamingMap(d.IMSI, d.HPLMN))
	})

	r.Get("/api/disaster-roaming/roaming-ues", func(w http.ResponseWriter, _ *http.Request) {
		list, err := disaster_roaming.GetDisasterRoamingUEs()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []map[string]interface{}{}
		}
		jsonReply(w, list)
	})

	r.Post("/api/disaster-roaming/release", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI  string `json:"imsi"`
			HPLMN string `json:"hplmn"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.IMSI == "" || d.HPLMN == "" {
			jsonError(w, "imsi and hplmn required", http.StatusBadRequest)
			return
		}
		if err := disaster_roaming.ReleaseRoamingUE(d.IMSI, d.HPLMN); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})

	// ── Audit log ────────────────────────────────────────────────
	r.Get("/api/disaster-roaming/log", func(w http.ResponseWriter, rq *http.Request) {
		limit := 100
		if v := rq.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		list, err := disaster_roaming.GetDRLog(limit)
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

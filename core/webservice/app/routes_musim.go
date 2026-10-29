// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_musim.go — REST surface for Multi-USIM (MUSIM).
//
// Wires `nf/amf/musim` to /api/musim/*. Before this surface landed
// the musim.html panel called these endpoints into the void — the
// route block did not exist. The package itself shipped only a
// stub `List()` that returned every row in musim_groups untyped.
//
// Spec anchors:
//
//   - TS 23.501 §5.34       — System support for Multi-USIM
//                             devices.
//   - TS 23.502 §4.2.6      — Multi-USIM procedures (paging /
//                             busy indication / pre-emption).
//   - TS 24.501 §9.11.3.91  — MUSIM Allowed Indication NAS IE.
//
// All response shapes are `{ok: true, ...}` envelopes.
package app

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/mmt/mmt-studio-core/nf/amf/musim"
)

func (s *Server) registerMUSIMRoutes() {
	r := s.Router

	// ── Dashboard ────────────────────────────────────────────────
	r.Get("/api/musim/stats", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, map[string]any{"ok": true, "stats": musim.Stats()})
	})

	// ── Groups CRUD (TS 23.501 §5.34) ────────────────────────────
	r.Get("/api/musim/groups", func(w http.ResponseWriter, _ *http.Request) {
		groups, err := musim.ListGroups()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{
			"ok":     true,
			"groups": groups,
			"count":  len(groups),
		})
	})

	r.Get("/api/musim/groups/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "id must be integer", http.StatusBadRequest)
			return
		}
		g, err := musim.GetGroup(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if g == nil {
			jsonError(w, "group not found", http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "group": g})
	})

	r.Post("/api/musim/groups", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			DeviceID    string   `json:"device_id"`
			Description string   `json:"description"`
			IMSIs       []string `json:"imsis"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		id, err := musim.CreateGroup(d.DeviceID, d.Description, d.IMSIs)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "id": id})
	})

	r.Patch("/api/musim/groups/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "id must be integer", http.StatusBadRequest)
			return
		}
		var patch map[string]any
		if !decodeJSON(w, rq, &patch) {
			return
		}
		ok, err := musim.UpdateGroup(id, patch)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		if !ok {
			jsonError(w, "group not found", http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "id": id})
	})

	r.Delete("/api/musim/groups/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "id must be integer", http.StatusBadRequest)
			return
		}
		ok, err := musim.DeleteGroup(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			jsonError(w, "group not found", http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "id": id})
	})

	// ── Members ──────────────────────────────────────────────────
	r.Post("/api/musim/groups/{id}/members", func(w http.ResponseWriter, rq *http.Request) {
		gid, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "id must be integer", http.StatusBadRequest)
			return
		}
		var d struct {
			IMSI      string `json:"imsi"`
			Priority  int    `json:"priority"`
			USIMIndex int    `json:"usim_index"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		mid, err := musim.AddMember(gid, d.IMSI, d.Priority, d.USIMIndex)
		if err != nil {
			code := http.StatusBadRequest
			if err.Error() == "group not found" {
				code = http.StatusNotFound
			}
			jsonError(w, err.Error(), code)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "id": mid, "group_id": gid})
	})

	// Convenience activate endpoint — sets group.active_imsi via
	// the same UpdateGroup path as PATCH /groups/{id}. Tests and
	// MUSIM device-management tooling treat USIM selection as a
	// distinct operation (TS 23.501 §5.34 calls out USIM
	// activation as a first-class state change), so this gives
	// the operation its own verb without duplicating logic.
	r.Post("/api/musim/groups/{id}/activate", func(w http.ResponseWriter, rq *http.Request) {
		gid, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "id must be integer", http.StatusBadRequest)
			return
		}
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
		ok, err := musim.UpdateGroup(gid, map[string]any{
			"active_imsi": d.IMSI,
		})
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		if !ok {
			jsonError(w, "group not found", http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{
			"ok":           true,
			"group_id":     gid,
			"active_imsi":  d.IMSI,
		})
	})

	r.Delete("/api/musim/members/{mid}", func(w http.ResponseWriter, rq *http.Request) {
		mid, err := strconv.ParseInt(chi.URLParam(rq, "mid"), 10, 64)
		if err != nil {
			jsonError(w, "mid must be integer", http.StatusBadRequest)
			return
		}
		ok, err := musim.RemoveMember(mid)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			jsonError(w, "member not found", http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "id": mid})
	})

	// ── Capabilities (TS 24.501 §9.11.3.91) ──────────────────────
	r.Get("/api/musim/capabilities", func(w http.ResponseWriter, _ *http.Request) {
		caps, err := musim.ListCapabilities()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{
			"ok":           true,
			"capabilities": caps,
			"count":        len(caps),
		})
	})

	r.Post("/api/musim/capabilities", func(w http.ResponseWriter, rq *http.Request) {
		// Accept `musim_supported` as either bool or 0/1 numeric —
		// TS 24.501 §9.11.3.91 encodes the capability as a single
		// bit on the wire, so both spellings are valid clients of
		// the operator API.
		var d struct {
			IMSI                string `json:"imsi"`
			MUSIMSupported      any    `json:"musim_supported"`
			MaxUSIMCount        int    `json:"max_usim_count"`
			MinPagingIntervalMS int    `json:"min_paging_interval_ms"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		supported := false
		switch v := d.MUSIMSupported.(type) {
		case bool:
			supported = v
		case float64:
			supported = v != 0
		case string:
			supported = v == "true" || v == "1"
		}
		if err := musim.UpsertCapability(d.IMSI, supported,
			d.MaxUSIMCount, d.MinPagingIntervalMS); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "imsi": d.IMSI})
	})

	// ── Paging audit + simulator (TS 23.502 §4.2.6) ──────────────
	r.Get("/api/musim/paging-log", func(w http.ResponseWriter, rq *http.Request) {
		dev := rq.URL.Query().Get("device_id")
		limit := 200
		if v := rq.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		entries, err := musim.ListPagingLog(dev, limit)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{
			"ok":    true,
			"log":   entries,
			"count": len(entries),
		})
	})

	r.Post("/api/musim/page", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			DeviceID   string `json:"device_id"`
			TargetIMSI string `json:"target_imsi"`
			Reason     string `json:"reason"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		res, err := musim.Page(d.DeviceID, d.TargetIMSI, d.Reason)
		if err != nil {
			code := http.StatusBadRequest
			if err.Error() == "group not found for device_id "+d.DeviceID {
				code = http.StatusNotFound
			}
			jsonError(w, err.Error(), code)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "result": res})
	})
}

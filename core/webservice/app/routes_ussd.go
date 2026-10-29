// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_ussd.go — REST surface for Unstructured Supplementary Service
// Data (USSD).
//
// Wires `services/ussd` to /api/ussd/*. The package owns the
// `ussd_menus` tree, the `ussd_sessions` lifecycle, and the in-memory
// active-session state used by the interactive menu walker.
//
// Spec anchors:
//
//   - TS 24.390 §4.2 — USSD over IMS (initiate / continue / release).
//   - TS 22.090       — Stage-1 USSD service definition.
//   - TS 22.030 §6.5  — Man-Machine Interface; USSD strings end with #.
//
// All response shapes are `{ok: true, ...}` envelopes; the legacy
// `{session_id, type, text, ended}` payload from the package is
// nested under `result` to keep both shapes navigable from the GUI.
package app

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/mmt/mmt-studio-core/services/ussd"
)

func (s *Server) registerUSSDRoutes() {
	r := s.Router

	// ── Status / dashboard ────────────────────────────────────────
	r.Get("/api/ussd/status", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, map[string]any{"ok": true, "status": ussd.Status()})
	})

	// ── Menu tree CRUD (operator authoring surface) ───────────────
	r.Get("/api/ussd/menus", func(w http.ResponseWriter, _ *http.Request) {
		menus, err := ussd.ListMenus()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if menus == nil {
			menus = []ussd.Menu{}
		}
		jsonReply(w, map[string]any{
			"ok": true, "menus": menus, "count": len(menus),
		})
	})

	r.Post("/api/ussd/menus", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			Code         string `json:"code"`
			Title        string `json:"title"`
			ParentID     *int64 `json:"parent_id"`
			ActionType   string `json:"action_type"`
			ActionData   string `json:"action_data"`
			DisplayOrder int    `json:"display_order"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.Title == "" {
			jsonError(w, "title required", http.StatusBadRequest)
			return
		}
		// Top-level menus carry a code (the *xxx# string); child
		// menus inherit selection by display_order. Reject "code
		// without parent_id" only when both are missing — the
		// schema CHECK enforces the actual rule.
		if d.Code == "" && d.ParentID == nil {
			jsonError(w, "code required for top-level menu",
				http.StatusBadRequest)
			return
		}
		var codePtr *string
		if d.Code != "" {
			c := d.Code
			codePtr = &c
		}
		id, err := ussd.CreateMenu(codePtr, d.Title, d.ParentID,
			d.ActionType, d.ActionData, d.DisplayOrder)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "id": id})
	})

	r.Patch("/api/ussd/menus/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "id must be integer", http.StatusBadRequest)
			return
		}
		var d map[string]interface{}
		if !decodeJSON(w, rq, &d) {
			return
		}
		// Allow-list keys to match the package's UpdateMenu contract.
		allowed := map[string]bool{
			"title": true, "code": true, "action_type": true,
			"action_data": true, "display_order": true, "parent_id": true,
		}
		patch := map[string]interface{}{}
		for k, v := range d {
			if allowed[k] {
				patch[k] = v
			}
		}
		if len(patch) == 0 {
			jsonError(w, "no allowed fields in patch",
				http.StatusBadRequest)
			return
		}
		if err := ussd.UpdateMenu(id, patch); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "id": id})
	})

	r.Delete("/api/ussd/menus/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "id must be integer", http.StatusBadRequest)
			return
		}
		if err := ussd.DeleteMenu(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "id": id})
	})

	// ── Seed the default menu tree (one-shot) ────────────────────
	r.Post("/api/ussd/menus/seed", func(w http.ResponseWriter, _ *http.Request) {
		ussd.SeedDefaultMenus()
		jsonReply(w, map[string]any{
			"ok": true, "menu_count": ussd.MenuCount(),
		})
	})

	// ── Session lifecycle (TS 24.390 §4.2) ───────────────────────
	r.Post("/api/ussd/session", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI       string `json:"imsi"`
			USSDString string `json:"ussd_string"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.IMSI == "" || d.USSDString == "" {
			jsonError(w, "imsi and ussd_string required",
				http.StatusBadRequest)
			return
		}
		result := ussd.InitiateSession(d.IMSI, d.USSDString)
		if errMsg, ok := result["error"].(string); ok {
			jsonError(w, errMsg, http.StatusBadRequest)
			return
		}
		// Spread the result keys at the top level alongside ok=true
		// so existing tester / panel code keeps reading them.
		out := map[string]any{"ok": true}
		for k, v := range result {
			out[k] = v
		}
		jsonReply(w, out)
	})

	// In-session navigation. TS 24.390 §4.5.4 "Invocation and
	// operation (user initiated)" describes the SIP-side flow; the
	// REST verb is implementation-local. Two URL forms are accepted
	// — /continue (panel convention) and /respond (tester / GSM-SS
	// MMI convention) — both call the same ContinueSession handler.
	// Payload accepts either `input` or `response` for the user's
	// reply text so neither caller has to know the other's shape.
	continueHandler := func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "id must be integer", http.StatusBadRequest)
			return
		}
		var d struct {
			Input    string `json:"input"`
			Response string `json:"response"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		reply := d.Input
		if reply == "" {
			reply = d.Response
		}
		result := ussd.ContinueSession(id, reply)
		if errMsg, ok := result["error"].(string); ok {
			jsonError(w, errMsg, http.StatusBadRequest)
			return
		}
		out := map[string]any{"ok": true}
		for k, v := range result {
			out[k] = v
		}
		jsonReply(w, out)
	}
	r.Post("/api/ussd/session/{id}/continue", continueHandler)
	r.Post("/api/ussd/session/{id}/respond", continueHandler)

	r.Post("/api/ussd/session/{id}/end", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "id must be integer", http.StatusBadRequest)
			return
		}
		ussd.EndSession(id)
		jsonReply(w, map[string]any{"ok": true, "id": id})
	})

	r.Get("/api/ussd/sessions", func(w http.ResponseWriter, rq *http.Request) {
		imsi := rq.URL.Query().Get("imsi")
		state := rq.URL.Query().Get("state")
		sessions, err := ussd.ListSessions(imsi, state)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if sessions == nil {
			sessions = []ussd.Session{}
		}
		jsonReply(w, map[string]any{
			"ok": true, "sessions": sessions, "count": len(sessions),
		})
	})
}

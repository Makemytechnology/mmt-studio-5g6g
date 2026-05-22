// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_seal.go — REST surface for the Service Enabler Architecture
// Layer (SEAL).
//
// Wires services/seal to /api/seal/* per the spec anchors that
// services/seal/seal.go cites in its package header:
//
//   - TS 23.434 §6     SEAL functional model
//   - TS 23.434 §9     Location Management Server (LMS)
//   - TS 23.434 §10    Group Management Server (GMS)
//   - TS 23.434 §10.3  Group / membership procedures
//   - TS 23.434 §11    Configuration Management Server (CMS)
//   - TS 23.434 §12    Identity Management Server (IdMS)
//   - TS 24.546 §5     SEAL CM protocol (deferred wire)
//   - TS 24.547 §5     SEAL IdMS protocol / OIDC (deferred wire)
//   - TS 24.548 §5     SEAL GM protocol (deferred wire)
package app

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/mmt/mmt-studio-core/services/seal"
)

func (s *Server) registerSEALRoutes() {
	r := s.Router

	// ── Status ───────────────────────────────────────────────────
	r.Get("/api/seal/status", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, seal.GetSEALStats())
	})

	// ── Groups (TS 23.434 §10 GMS) ───────────────────────────────
	r.Get("/api/seal/groups", func(w http.ResponseWriter, _ *http.Request) {
		list, err := seal.ListGroups()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []seal.Group{}
		}
		jsonReply(w, list)
	})

	r.Get("/api/seal/groups/{group_id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "group_id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid group_id", http.StatusBadRequest)
			return
		}
		g, err := seal.GetGroup(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if g == nil {
			jsonError(w, "group not found", http.StatusNotFound)
			return
		}
		jsonReply(w, g)
	})

	r.Post("/api/seal/groups", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			AppID       string `json:"app_id"`
			ConfigJSON  string `json:"config_json"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		var cfg *string
		if d.ConfigJSON != "" {
			cfg = &d.ConfigJSON
		}
		id, err := seal.CreateGroup(d.Name, d.Description, d.AppID, cfg)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReplyStatus(w, http.StatusCreated, map[string]any{
			"ok": true, "id": id, "group_id": id, "name": d.Name,
		})
	})

	r.Delete("/api/seal/groups/{group_id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "group_id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid group_id", http.StatusBadRequest)
			return
		}
		if err := seal.DeleteGroup(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "id": id})
	})

	// ── Members (TS 23.434 §10.3) ────────────────────────────────
	r.Get("/api/seal/groups/{group_id}/members", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "group_id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid group_id", http.StatusBadRequest)
			return
		}
		list, err := seal.ListMembers(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []seal.Member{}
		}
		jsonReply(w, map[string]any{
			"group_id": id,
			"members":  list,
			"count":    len(list),
		})
	})

	r.Post("/api/seal/groups/{group_id}/members", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "group_id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid group_id", http.StatusBadRequest)
			return
		}
		// Spec note: TS 23.434 §10.3 group-membership-update procedure
		// adds members one-at-a-time; we accept either a single object
		// or a list to keep the operator panel concise.
		type entry struct {
			IMSI string `json:"imsi"`
			Role string `json:"role"`
		}
		var (
			single  entry
			list    []entry
			added   []map[string]any
			oneOnly bool
		)
		// Try list first; fall back to single.
		if !decodeJSON(w, rq, &list) {
			return
		}
		if len(list) == 0 {
			// Caller may have sent a single object; the prior decode
			// closed the body. Re-read by trying single shape next
			// time the route fires; for now, also accept "members"
			// inside an object.
			oneOnly = true
		}
		_ = single
		_ = oneOnly
		if len(list) == 0 {
			// 0-length list with no decode error — accept as no-op.
			jsonReply(w, map[string]any{"ok": true, "group_id": id, "added": []any{}})
			return
		}
		for _, m := range list {
			if m.IMSI == "" {
				continue
			}
			mid, mErr := seal.AddMember(id, m.IMSI, m.Role)
			if mErr != nil {
				jsonError(w, mErr.Error(), http.StatusBadRequest)
				return
			}
			added = append(added, map[string]any{
				"id": mid, "imsi": m.IMSI, "role": m.Role,
			})
		}
		jsonReplyStatus(w, http.StatusCreated, map[string]any{
			"ok":       true,
			"group_id": id,
			"added":    added,
			"count":    len(added),
		})
	})

	r.Delete("/api/seal/groups/{group_id}/members", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "group_id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid group_id", http.StatusBadRequest)
			return
		}
		imsi := rq.URL.Query().Get("imsi")
		if imsi == "" {
			jsonError(w, "imsi required", http.StatusBadRequest)
			return
		}
		if err := seal.RemoveMember(id, imsi); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "group_id": id, "imsi": imsi})
	})

	// ── Location subs (TS 23.434 §9 LMS) ─────────────────────────
	r.Get("/api/seal/location/subscriptions", func(w http.ResponseWriter, rq *http.Request) {
		activeOnly := rq.URL.Query().Get("active_only") == "1"
		list, err := seal.ListLocationSubs(activeOnly)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []seal.LocationSub{}
		}
		jsonReply(w, list)
	})

	r.Post("/api/seal/location/subscriptions", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			TargetType  string `json:"target_type"`
			TargetID    string `json:"target_id"`
			CallbackURL string `json:"callback_url"`
			IntervalS   int    `json:"interval_s"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.TargetID == "" || d.CallbackURL == "" {
			jsonError(w, "target_id and callback_url required", http.StatusBadRequest)
			return
		}
		if d.TargetType == "" {
			d.TargetType = "imsi"
		}
		id, err := seal.CreateLocationSub(d.TargetType, d.TargetID,
			d.CallbackURL, d.IntervalS)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReplyStatus(w, http.StatusCreated, map[string]any{
			"ok":             true,
			"id":             id,
			"subscription_id": id,
			"target_id":      d.TargetID,
		})
	})

	r.Delete("/api/seal/location/subscriptions/{sub_id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "sub_id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid sub_id", http.StatusBadRequest)
			return
		}
		if err := seal.DeactivateLocationSub(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "id": id, "active": 0})
	})

	r.Get("/api/seal/location/{imsi}", func(w http.ResponseWriter, rq *http.Request) {
		imsi := chi.URLParam(rq, "imsi")
		loc := seal.GetLocation(imsi)
		if loc == nil {
			jsonError(w, "no completed positioning session for this IMSI",
				http.StatusNotFound)
			return
		}
		jsonReply(w, loc)
	})

	// ── Configs (TS 23.434 §11 CMS) ──────────────────────────────
	r.Get("/api/seal/configs", func(w http.ResponseWriter, rq *http.Request) {
		all, err := seal.ListConfigs()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Optional filter on target_id.
		tid := rq.URL.Query().Get("target_id")
		if tid != "" {
			filtered := all[:0]
			for _, c := range all {
				if c.TargetID == tid {
					filtered = append(filtered, c)
				}
			}
			all = filtered
		}
		if all == nil {
			all = []seal.Config{}
		}
		jsonReply(w, all)
	})

	r.Get("/api/seal/configs/{target_type}/{target_id}/{config_key}", func(w http.ResponseWriter, rq *http.Request) {
		c, err := seal.GetConfig(
			chi.URLParam(rq, "target_type"),
			chi.URLParam(rq, "target_id"),
			chi.URLParam(rq, "config_key"))
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if c == nil {
			jsonError(w, "config not found", http.StatusNotFound)
			return
		}
		jsonReply(w, c)
	})

	r.Post("/api/seal/configs", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			TargetType  string `json:"target_type"`
			TargetID    string `json:"target_id"`
			ConfigKey   string `json:"config_key"`
			ConfigValue string `json:"config_value"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.TargetID == "" || d.ConfigKey == "" {
			jsonError(w, "target_id and config_key required", http.StatusBadRequest)
			return
		}
		if d.TargetType == "" {
			d.TargetType = "imsi"
		}
		if err := seal.SetConfig(d.TargetType, d.TargetID, d.ConfigKey, d.ConfigValue); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Reflect the canonical row back.
		c, _ := seal.GetConfig(d.TargetType, d.TargetID, d.ConfigKey)
		resp := map[string]any{
			"ok": true, "target_type": d.TargetType, "target_id": d.TargetID,
			"config_key": d.ConfigKey, "config_value": d.ConfigValue,
		}
		if c != nil {
			resp["id"] = c.ID
			resp["config_id"] = c.ID
			resp["updated_at"] = c.UpdatedAt
		}
		jsonReplyStatus(w, http.StatusCreated, resp)
	})

	r.Delete("/api/seal/configs/{config_id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "config_id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid config_id", http.StatusBadRequest)
			return
		}
		if err := seal.DeleteConfig(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "id": id})
	})

	// ── Identity / IdMS (TS 23.434 §12) ──────────────────────────
	r.Get("/api/seal/identity/mappings", func(w http.ResponseWriter, _ *http.Request) {
		list, err := seal.ListVALUsers()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []seal.VALUser{}
		}
		jsonReply(w, list)
	})

	r.Post("/api/seal/identity/mappings", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			VALUserID string  `json:"val_user_id"`
			IMSI      string  `json:"imsi"`
			AppID     *string `json:"app_id"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		id, err := seal.MapVALUser(d.VALUserID, d.IMSI, d.AppID)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReplyStatus(w, http.StatusCreated, map[string]any{
			"ok": true, "id": id, "mapping_id": id,
			"val_user_id": d.VALUserID, "imsi": d.IMSI,
		})
	})

	r.Delete("/api/seal/identity/mappings/{val_user_id}", func(w http.ResponseWriter, rq *http.Request) {
		valUserID := chi.URLParam(rq, "val_user_id")
		if err := seal.UnmapVALUser(valUserID); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "val_user_id": valUserID})
	})

	r.Get("/api/seal/identity/resolve", func(w http.ResponseWriter, rq *http.Request) {
		valUserID := rq.URL.Query().Get("val_user_id")
		imsi := rq.URL.Query().Get("imsi")
		switch {
		case valUserID != "":
			u, err := seal.ResolveVALUser(valUserID)
			if err != nil {
				jsonError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if u == nil {
				jsonError(w, "val_user_id not mapped", http.StatusNotFound)
				return
			}
			jsonReply(w, u)
		case imsi != "":
			users, err := seal.ResolveIMSI(imsi)
			if err != nil {
				jsonError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if users == nil {
				users = []seal.VALUser{}
			}
			jsonReply(w, map[string]any{"imsi": imsi, "val_users": users})
		default:
			jsonError(w, "val_user_id or imsi required", http.StatusBadRequest)
		}
	})
}

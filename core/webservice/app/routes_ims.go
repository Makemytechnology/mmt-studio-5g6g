// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Auto-extracted from domain_routes.go (refactor: split god function by
// domain banner). Do not re-merge — keep new domain APIs in their own
// routes_<domain>.go file.
package app

import (
	"net/http"

	"github.com/mmt/mmt-studio-core/db/engine"
)

func (s *Server) registerIMSRoutes() {
	r := s.Router

	// ── IMS ──────────────────────────────────────────────────────────
	r.Get("/api/ims/subscribers", func(w http.ResponseWriter, rq *http.Request) {
		db, err := engine.Open()
		if err != nil {
			jsonReply(w, map[string]any{"items": []any{}})
			return
		}
		rows, err := db.Query(`SELECT ims.impi, ims.impu, u.imsi, u.msisdn
			FROM ims_subscribers ims
			JOIN ue u ON u.id = ims.ue_id ORDER BY u.imsi`)
		if err != nil {
			jsonReply(w, map[string]any{"items": []any{}})
			return
		}
		defer rows.Close()
		var items []map[string]any
		for rows.Next() {
			var impi, impu, imsi string
			var msisdn *string
			if rows.Scan(&impi, &impu, &imsi, &msisdn) == nil {
				items = append(items, map[string]any{
					"impi": impi, "impu": impu, "imsi": imsi, "msisdn": msisdn,
				})
			}
		}
		if items == nil {
			items = []map[string]any{}
		}
		jsonReply(w, map[string]any{"items": items})
	})
	r.Post("/api/ims/subscribers", func(w http.ResponseWriter, rq *http.Request) {
		// Stub: IMS HSS not yet ported
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
		jsonReply(w, map[string]any{"imsi": d.IMSI, "status": "stub_created"})
	})
	r.Delete("/api/ims/subscribers", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMPI string `json:"impi"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.IMPI == "" {
			jsonError(w, "impi required", http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "deleted": 0})
	})
	r.Get("/api/ims/registrations", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"items": []any{}})
	})
	r.Get("/api/ims/calls", func(w http.ResponseWriter, rq *http.Request) {
		db, err := engine.Open()
		if err != nil {
			jsonReply(w, map[string]any{"items": []any{}})
			return
		}
		rows, err := db.Query(`SELECT * FROM ims_dialogs WHERE state != 'TERMINATED' ORDER BY id`)
		if err != nil {
			jsonReply(w, map[string]any{"items": []any{}})
			return
		}
		defer rows.Close()
		cols, _ := rows.Columns()
		var items []map[string]any
		for rows.Next() {
			scan := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range scan {
				ptrs[i] = &scan[i]
			}
			if rows.Scan(ptrs...) == nil {
				row := make(map[string]any, len(cols))
				for i, name := range cols {
					row[name] = scan[i]
				}
				items = append(items, row)
			}
		}
		if items == nil {
			items = []map[string]any{}
		}
		jsonReply(w, map[string]any{"items": items})
	})
	r.Get("/api/ims/conferences", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"items": []any{}})
	})
	r.Post("/api/ims/conferences", func(w http.ResponseWriter, rq *http.Request) {
		// Conference AS not yet ported
		jsonError(w, "Conference AS not available", http.StatusServiceUnavailable)
	})
	r.Delete("/api/ims/conferences/{conf_id}", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"ok": true})
	})
	r.Get("/api/ims/mrfp/sessions", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"items": map[string]any{}})
	})
}

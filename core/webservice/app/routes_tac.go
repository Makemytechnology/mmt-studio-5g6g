// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Auto-extracted from domain_routes.go (refactor: split god function by
// domain banner). Do not re-merge — keep new domain APIs in their own
// routes_<domain>.go file.
package app

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/infra/tac"
)

func (s *Server) registerTACRoutes() {
	r := s.Router

	// ── TAC CRUD ─────────────────────────────────────────────────────
	r.Get("/api/tac", func(w http.ResponseWriter, rq *http.Request) {
		list, err := tac.List(false)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, list)
	})
	r.Post("/api/tac", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			TAC  string `json:"tac"`
			MCC  string `json:"plmn_mcc"`
			MNC  string `json:"plmn_mnc"`
			Name string `json:"name"`
			Prio int    `json:"paging_priority"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if err := tac.Create(d.TAC, d.MCC, d.MNC, d.Name, d.Prio); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]bool{"ok": true})
	})
	r.Delete("/api/tac/{tac}", func(w http.ResponseWriter, rq *http.Request) {
		n, _ := tac.Delete(chi.URLParam(rq, "tac"))
		jsonReply(w, map[string]int64{"deleted": n})
	})

	// TAC sub-routes expected by the frontend
	r.Get("/api/tac/tracking-areas", func(w http.ResponseWriter, rq *http.Request) {
		list, err := tac.List(false)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			jsonReply(w, []any{})
			return
		}
		// Convert to frontend-expected shape
		var out []map[string]any
		for _, t := range list {
			out = append(out, map[string]any{
				"tac": t.TAC, "plmn_mcc": t.PLMNMCC, "plmn_mnc": t.PLMNMNC,
				"name": t.Name, "paging_priority": t.PagingPriority, "enabled": t.Enabled,
			})
		}
		jsonReply(w, out)
	})
	r.Post("/api/tac/tracking-areas", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			TAC  string `json:"tac"`
			MCC  string `json:"plmn_mcc"`
			MNC  string `json:"plmn_mnc"`
			Name string `json:"name"`
			Prio int    `json:"paging_priority"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if err := tac.Create(d.TAC, d.MCC, d.MNC, d.Name, d.Prio); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "tac": d.TAC})
	})
	r.Get("/api/tac/tracking-areas/{tac}", func(w http.ResponseWriter, rq *http.Request) {
		tacID := chi.URLParam(rq, "tac")
		t, err := tac.Get(tacID)
		if err != nil || t == nil {
			jsonReply(w, map[string]any{"tac": tacID, "gnbs": []any{}, "cells": []any{}})
			return
		}
		// Load gnb/cell mappings
		db, _ := engine.Open()
		gnbs := []string{}
		cells := []string{}
		if db != nil {
			gRows, err := db.Query(`SELECT gnb_id FROM ta_gnb_map WHERE tac=?`, tacID)
			if err == nil {
				for gRows.Next() {
					var g string
					if gRows.Scan(&g) == nil {
						gnbs = append(gnbs, g)
					}
				}
				gRows.Close()
			}
			cRows, err := db.Query(`SELECT cell_id FROM ta_cell_map WHERE tac=?`, tacID)
			if err == nil {
				for cRows.Next() {
					var c string
					if cRows.Scan(&c) == nil {
						cells = append(cells, c)
					}
				}
				cRows.Close()
			}
		}
		jsonReply(w, map[string]any{"tac": tacID, "gnbs": gnbs, "cells": cells})
	})
	r.Delete("/api/tac/tracking-areas/{tac}", func(w http.ResponseWriter, rq *http.Request) {
		n, _ := tac.Delete(chi.URLParam(rq, "tac"))
		jsonReply(w, map[string]int64{"deleted": n})
	})

	// TAC NSSAI policies. TACs are stored uppercase by tac.Create; the
	// FK on ta_nssai_policy.tac → tracking_areas.tac must therefore
	// match the same case, or the INSERT silently violates and yields
	// an empty listing. Normalise to upper-case at every entry point.
	r.Get("/api/tac/tracking-areas/{tac}/nssai", func(w http.ResponseWriter, rq *http.Request) {
		tacID := strings.ToUpper(chi.URLParam(rq, "tac"))
		db, _ := engine.Open()
		var out []map[string]any
		if db != nil {
			rows, err := db.Query(`SELECT sst, sd, allowed FROM ta_nssai_policy WHERE tac=?`, tacID)
			if err == nil {
				for rows.Next() {
					var sst, allowed int
					var sd string
					if rows.Scan(&sst, &sd, &allowed) == nil {
						out = append(out, map[string]any{"sst": sst, "sd": sd, "allowed": allowed != 0})
					}
				}
				rows.Close()
			}
		}
		if out == nil {
			out = []map[string]any{}
		}
		jsonReply(w, out)
	})
	r.Post("/api/tac/tracking-areas/{tac}/nssai", func(w http.ResponseWriter, rq *http.Request) {
		tacID := strings.ToUpper(chi.URLParam(rq, "tac"))
		var d struct {
			SST     int     `json:"sst"`
			SD      *string `json:"sd"`
			Allowed bool    `json:"allowed"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		sd := ""
		if d.SD != nil {
			sd = *d.SD
		}
		al := 0
		if d.Allowed {
			al = 1
		}
		db, _ := engine.Open()
		// Surface FK / constraint failures so the GUI and tester can
		// see why an INSERT didn't land (previously swallowed silently).
		if _, err := db.Exec(
			`INSERT OR REPLACE INTO ta_nssai_policy (tac, sst, sd, allowed) VALUES (?,?,?,?)`,
			tacID, d.SST, sd, al,
		); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]bool{"ok": true})
	})
	r.Delete("/api/tac/tracking-areas/{tac}/nssai/{sst}", func(w http.ResponseWriter, rq *http.Request) {
		db, _ := engine.Open()
		tacID := strings.ToUpper(chi.URLParam(rq, "tac"))
		sd := rq.URL.Query().Get("sd")
		if sd != "" {
			db.Exec(`DELETE FROM ta_nssai_policy WHERE tac=? AND sst=? AND sd=?`, tacID, chi.URLParam(rq, "sst"), sd)
		} else {
			db.Exec(`DELETE FROM ta_nssai_policy WHERE tac=? AND sst=?`, tacID, chi.URLParam(rq, "sst"))
		}
		jsonReply(w, map[string]bool{"ok": true})
	})

	// TAC DNN policies
	r.Get("/api/tac/tracking-areas/{tac}/dnn", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, []map[string]any{})
	})
	r.Post("/api/tac/tracking-areas/{tac}/dnn", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]bool{"ok": true})
	})
	r.Delete("/api/tac/tracking-areas/{tac}/dnn/{dnn}", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]bool{"ok": true})
	})

	// Registration areas (TS 23.501 §5.4.2)
	r.Get("/api/tac/registration-areas", func(w http.ResponseWriter, rq *http.Request) {
		db, err := engine.Open()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		rows, err := db.Query(`SELECT id, name FROM registration_areas ORDER BY id`)
		if err != nil {
			jsonReply(w, []map[string]any{})
			return
		}
		var ras []map[string]any
		type raRow struct {
			id   int64
			name string
		}
		var raList []raRow
		for rows.Next() {
			var r raRow
			if rows.Scan(&r.id, &r.name) == nil {
				raList = append(raList, r)
			}
		}
		rows.Close()
		for _, ra := range raList {
			tRows, err := db.Query(`SELECT tac FROM registration_area_tas WHERE ra_id=?`, ra.id)
			var tacs []string
			if err == nil {
				for tRows.Next() {
					var t string
					if tRows.Scan(&t) == nil {
						tacs = append(tacs, t)
					}
				}
				tRows.Close()
			}
			ras = append(ras, map[string]any{"ra_id": ra.id, "name": ra.name, "tas": tacs})
		}
		if ras == nil {
			ras = []map[string]any{}
		}
		jsonReply(w, ras)
	})
	r.Post("/api/tac/registration-areas", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			Name string `json:"name"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		db, _ := engine.Open()
		res, err := db.Exec(`INSERT INTO registration_areas (name) VALUES (?)`, d.Name)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		id, _ := res.LastInsertId()
		jsonReply(w, map[string]any{"ra_id": id})
	})
	r.Delete("/api/tac/registration-areas/{id}", func(w http.ResponseWriter, rq *http.Request) {
		db, _ := engine.Open()
		db.Exec(`DELETE FROM registration_areas WHERE id=?`, chi.URLParam(rq, "id"))
		jsonReply(w, map[string]bool{"ok": true})
	})
	r.Post("/api/tac/registration-areas/{id}/tas", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			TAC string `json:"tac"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		db, _ := engine.Open()
		_, err := db.Exec(`INSERT OR IGNORE INTO registration_area_tas (ra_id, tac) VALUES (?,?)`,
			chi.URLParam(rq, "id"), d.TAC)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]bool{"ok": true})
	})
}

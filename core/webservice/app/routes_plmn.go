// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Auto-extracted from domain_routes.go (refactor: split god function by
// domain banner). Do not re-merge — keep new domain APIs in their own
// routes_<domain>.go file.
package app

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/mmt/mmt-studio-core/db/crud"
	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/infra/plmn"
	"github.com/mmt/mmt-studio-core/nf/amf"
)

func (s *Server) registerPLMNRoutes() {
	r := s.Router

	// ── PLMN CRUD ────────────────────────────────────────────────────
	r.Get("/api/plmn/supported", func(w http.ResponseWriter, rq *http.Request) {
		list, err := plmn.List(false)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []plmn.PLMN{}
		}
		// Enrich with NSSAI per PLMN
		db, _ := engine.Open()
		var result []map[string]any
		for _, p := range list {
			m := map[string]any{
				"plmn_id": p.PLMNID, "mcc": p.MCC, "mnc": p.MNC,
				"name": p.Name, "plmn_type": p.Type, "priority": p.Priority,
				"enabled":       p.Enabled,
				"amf_region_id": nil, "amf_set_id": nil, "amf_pointer": nil,
			}
			if p.AMFRegionID.Valid {
				m["amf_region_id"] = p.AMFRegionID.Int64
			}
			if p.AMFSetID.Valid {
				m["amf_set_id"] = p.AMFSetID.Int64
			}
			if p.AMFPointer.Valid {
				m["amf_pointer"] = p.AMFPointer.Int64
			}
			// Load NSSAI
			nssai := []map[string]any{}
			if db != nil {
				nRows, err := db.Query(`SELECT sst, sd FROM plmn_nssai WHERE plmn_id=? ORDER BY sst`, p.PLMNID)
				if err == nil {
					for nRows.Next() {
						var sst int
						var sd string
						if nRows.Scan(&sst, &sd) == nil {
							entry := map[string]any{"sst": sst}
							if sd != "" {
								entry["sd"] = sd
							}
							nssai = append(nssai, entry)
						}
					}
					nRows.Close()
				}
			}
			m["nssai"] = nssai
			result = append(result, m)
		}
		jsonReply(w, result)
	})
	r.Post("/api/plmn/supported", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			MCC         string `json:"mcc"`
			MNC         string `json:"mnc"`
			Name        string `json:"name"`
			Type        string `json:"plmn_type"`
			Priority    int    `json:"priority"`
			AMFRegionID *int64 `json:"amf_region_id"`
			AMFSetID    *int64 `json:"amf_set_id"`
			AMFPointer  *int64 `json:"amf_pointer"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		p := plmn.PLMN{
			MCC: d.MCC, MNC: d.MNC, Name: d.Name,
			Type: d.Type, Priority: d.Priority, Enabled: true,
		}
		if d.AMFRegionID != nil {
			p.AMFRegionID.Valid = true
			p.AMFRegionID.Int64 = *d.AMFRegionID
		}
		if d.AMFSetID != nil {
			p.AMFSetID.Valid = true
			p.AMFSetID.Int64 = *d.AMFSetID
		}
		if d.AMFPointer != nil {
			p.AMFPointer.Valid = true
			p.AMFPointer.Int64 = *d.AMFPointer
		}
		if err := plmn.Upsert(p); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// A served PLMN must support at least one S-NSSAI: the NG
		// Setup Response's Slice Support List is SIZE(1..) per
		// TS 38.413 §9.3.1.17, and TS 23.501 §5.15 requires one or
		// more S-NSSAIs per PLMN. Seed the standardised eMBB slice
		// (SST=1, TS 23.501 Table 5.15.2.2-1) when the operator
		// didn't provision any — otherwise the response encode fails
		// and every NG Setup toward this PLMN is rejected.
		if db, err := engine.Open(); err == nil {
			plmnID := d.MCC + "-" + d.MNC
			var n int
			_ = db.QueryRow(`SELECT COUNT(*) FROM plmn_nssai WHERE plmn_id=?`, plmnID).Scan(&n)
			if n == 0 {
				_, _ = db.Exec(`INSERT OR REPLACE INTO plmn_nssai (plmn_id, sst, sd) VALUES (?,1,'')`, plmnID)
			}
		}
		// The AMF's served-PLMN / GUAMI view is a boot-time snapshot of
		// these tables (amf.InitContextFromDB). Rebuild it so NG Setup
		// PLMN matching (TS 38.413 §8.7.1) sees the change immediately
		// — without this, provisioning only took effect on restart.
		if err := amf.InitContextFromDB(); err != nil {
			jsonError(w, "plmn stored but AMF reload failed: "+err.Error(),
				http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]bool{"ok": true})
	})
	r.Delete("/api/plmn/supported/{plmnID}", func(w http.ResponseWriter, rq *http.Request) {
		id := chi.URLParam(rq, "plmnID")
		n, _ := plmn.Delete(id)
		if err := amf.InitContextFromDB(); err != nil {
			jsonError(w, "plmn deleted but AMF reload failed: "+err.Error(),
				http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]int64{"deleted": n})
	})

	// PLMN NSSAI
	r.Post("/api/plmn/supported/{plmnID}/nssai", func(w http.ResponseWriter, rq *http.Request) {
		plmnID := chi.URLParam(rq, "plmnID")
		var d struct {
			SST int     `json:"sst"`
			SD  *string `json:"sd"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		sd := ""
		if d.SD != nil {
			sd = *d.SD
		}
		db, err := engine.Open()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, err = db.Exec(`INSERT OR REPLACE INTO plmn_nssai (plmn_id, sst, sd) VALUES (?,?,?)`, plmnID, d.SST, sd)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := amf.InitContextFromDB(); err != nil {
			jsonError(w, "nssai stored but AMF reload failed: "+err.Error(),
				http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]bool{"ok": true})
	})
	r.Delete("/api/plmn/supported/{plmnID}/nssai/{sst}", func(w http.ResponseWriter, rq *http.Request) {
		plmnID := chi.URLParam(rq, "plmnID")
		sst := chi.URLParam(rq, "sst")
		sd := rq.URL.Query().Get("sd")
		db, _ := engine.Open()
		if sd != "" {
			db.Exec(`DELETE FROM plmn_nssai WHERE plmn_id=? AND sst=? AND sd=?`, plmnID, sst, sd)
		} else {
			db.Exec(`DELETE FROM plmn_nssai WHERE plmn_id=? AND sst=? AND sd=''`, plmnID, sst)
		}
		jsonReply(w, map[string]bool{"ok": true})
	})

	// Equivalent PLMN pairs
	r.Get("/api/plmn/equivalent", func(w http.ResponseWriter, rq *http.Request) {
		db, err := engine.Open()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		rows, err := db.Query(`SELECT home_plmn_id, equiv_plmn_id FROM equivalent_plmns`)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var out []map[string]string
		for rows.Next() {
			var h, e string
			if rows.Scan(&h, &e) == nil {
				out = append(out, map[string]string{"home_plmn_id": h, "equiv_plmn_id": e})
			}
		}
		rows.Close()
		if out == nil {
			out = []map[string]string{}
		}
		jsonReply(w, out)
	})
	r.Post("/api/plmn/equivalent", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			Home  string `json:"home_plmn_id"`
			Equiv string `json:"equiv_plmn_id"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		db, _ := engine.Open()
		_, err := db.Exec(`INSERT OR REPLACE INTO equivalent_plmns (home_plmn_id, equiv_plmn_id) VALUES (?,?)`, d.Home, d.Equiv)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]bool{"ok": true})
	})
	r.Delete("/api/plmn/equivalent/{home}/{equiv}", func(w http.ResponseWriter, rq *http.Request) {
		db, _ := engine.Open()
		db.Exec(`DELETE FROM equivalent_plmns WHERE home_plmn_id=? AND equiv_plmn_id=?`,
			chi.URLParam(rq, "home"), chi.URLParam(rq, "equiv"))
		jsonReply(w, map[string]bool{"ok": true})
	})

	// ── NSSAI (slices panel) ─────────────────────────────────────────
	r.Get("/api/slices", func(w http.ResponseWriter, rq *http.Request) {
		list, err := crud.NSSAICatalogList()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, list)
	})
}
